package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"rsc.io/qr"

	"taptimer/internal/rooms"
)

// Online rooms: the HTTP and WebSocket glue between browsers and
// internal/rooms. Every change to a room sends all of its players a fresh
// snapshot ({"t":"state"}); players send small commands back.
//
// Rooms live in this process's memory, so the app must run on one machine
// (fly scale count 1) until rooms are routed between machines.

const (
	wsReadLimit    = 1024             // bytes per message
	wsPingEvery    = 25 * time.Second // keeps proxies from dropping idle sockets
	wsWriteTimeout = 10 * time.Second
	sendBuffer     = 16 // queued messages per client before it's dropped as too slow

	msgRate  = 10 // messages per second per connection...
	msgBurst = 20 // ...with this much burst

	createsPerMinute = 10 // room creations per IP
	joinsPerMinute   = 30 // WebSocket connections per IP
)

var (
	roomCodeRe = regexp.MustCompile(`^[23456789ABCDEFGHJKMNPQRSTUVWXYZ]{4}$`)
	tokenRe    = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
)

type online struct {
	hub     *rooms.Hub
	baseURL string
	now     func() time.Time
	tick    time.Duration

	mu       sync.Mutex
	clients  map[*rooms.Room]map[*client]struct{}
	sweeping bool
	closed   bool

	creates, joins *ipLimiter
}

type client struct {
	ws     *websocket.Conn
	room   *rooms.Room
	player string      // set once joined
	send   chan []byte // closed (under online.mu) to make the writer hang up
	closed bool        // guarded by online.mu

	tokens float64 // message rate limit
	last   time.Time
}

func newOnline(baseURL string) *online {
	return &online{
		hub:     rooms.NewHub(),
		baseURL: baseURL,
		now:     time.Now,
		tick:    time.Second,
		clients: make(map[*rooms.Room]map[*client]struct{}),
		creates: newIPLimiter(createsPerMinute, time.Minute),
		joins:   newIPLimiter(joinsPerMinute, time.Minute),
	}
}

// createRoom handles POST /api/rooms and returns {"code":"K7QX"}. The
// creator then joins over the WebSocket like everyone else.
func (o *online) createRoom(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !o.creates.allow(c.ClientIP(), o.now()) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate_limited"})
		return
	}
	r, err := o.hub.Create(o.now())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": errorCode(err)})
		return
	}
	o.startSweeper()
	slog.Info("room_create", "room", r.Code())
	c.JSON(http.StatusCreated, gin.H{"code": r.Code()})
}

// socket handles GET /ws/:code. Same-origin only: the library rejects
// cross-site Origin headers, and the canonical-host redirect means the
// page and socket always share a host.
func (o *online) socket(c *gin.Context) {
	if !o.joins.allow(c.ClientIP(), o.now()) {
		c.String(http.StatusTooManyRequests, "rate_limited")
		return
	}
	// The http.Server's read/write timeouts would otherwise carry over to the
	// hijacked connection and cut every socket off after a few seconds.
	rc := http.NewResponseController(c.Writer)
	if err := errors.Join(rc.SetReadDeadline(time.Time{}), rc.SetWriteDeadline(time.Time{})); err != nil {
		slog.Warn("ws_clear_deadlines", "err", err)
	}
	ws, err := websocket.Accept(c.Writer, c.Request, nil)
	if err != nil {
		return // Accept has already written the HTTP error
	}
	ws.SetReadLimit(wsReadLimit)

	room, err := o.hub.Get(c.Param("code"))
	if err != nil {
		o.refuse(ws, err)
		return
	}
	cl := &client{ws: ws, room: room, send: make(chan []byte, sendBuffer), tokens: msgBurst, last: o.now()}
	if !o.register(cl) {
		o.refuse(ws, errServerRestarting)
		return
	}
	go cl.writeLoop()
	o.readLoop(c.Request.Context(), cl)
	o.leave(cl)
}

var errServerRestarting = rooms.Error("server_restarting")

func (o *online) refuse(ws *websocket.Conn, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), wsWriteTimeout)
	defer cancel()
	_ = ws.Write(ctx, websocket.MessageText, errorMsg(err))
	_ = ws.Close(websocket.StatusNormalClosure, errorCode(err))
}

// message is every command a client can send; only the fields its type
// uses are read.
type message struct {
	T       string  `json:"t"`
	Name    string  `json:"name"`
	Token   string  `json:"token"`
	Goal    int     `json:"goal"`
	Rounds  int     `json:"rounds"`
	Round   int     `json:"round"`
	Elapsed float64 `json:"elapsed"`
}

func (o *online) readLoop(ctx context.Context, cl *client) {
	for {
		_, data, err := cl.ws.Read(ctx)
		if err != nil {
			return
		}
		now := o.now()
		if !cl.allow(now) {
			o.enqueue(cl, errorMsg(rooms.Error("rate_limited")))
			continue
		}
		var m message
		if json.Unmarshal(data, &m) != nil {
			o.enqueue(cl, errorMsg(rooms.Error("bad_message")))
			continue
		}
		if err := o.handle(cl, m, now); err != nil {
			o.enqueue(cl, errorMsg(err))
			continue
		}
		o.broadcast(cl.room)
	}
}

func (o *online) handle(cl *client, m message, now time.Time) error {
	r := cl.room
	if m.T == "join" {
		if cl.player != "" {
			return rooms.Error("already_joined")
		}
		if !tokenRe.MatchString(m.Token) {
			return rooms.Error("bad_token")
		}
		id, err := r.Join(m.Name, m.Token, now)
		if err != nil {
			return err
		}
		cl.player = id
		o.enqueue(cl, mustJSON(gin.H{"t": "welcome", "you": id}))
		return nil
	}
	if cl.player == "" {
		return rooms.Error("not_joined")
	}
	switch m.T {
	case "settings":
		return r.UpdateSettings(cl.player, rooms.Settings{Goal: m.Goal, Rounds: m.Rounds}, now)
	case "start":
		return r.Start(cl.player, now)
	case "result":
		return r.Submit(cl.player, m.Round, m.Elapsed, now)
	case "rematch":
		return r.Rematch(cl.player, now)
	}
	return rooms.Error("bad_message")
}

// broadcast sends the room's current state to everyone connected to it.
func (o *online) broadcast(r *rooms.Room) {
	msg := mustJSON(struct {
		T     string      `json:"t"`
		State rooms.State `json:"state"`
	}{"state", r.Snapshot(o.now())})
	o.mu.Lock()
	defer o.mu.Unlock()
	for cl := range o.clients[r] {
		o.enqueueLocked(cl, msg)
	}
}

func (o *online) register(cl *client) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return false
	}
	if o.clients[cl.room] == nil {
		o.clients[cl.room] = make(map[*client]struct{})
	}
	o.clients[cl.room][cl] = struct{}{}
	return true
}

// leave runs when a client's connection ends. Their seat is held for
// rooms.SeatHold so a reload or a dropped connection can rejoin.
func (o *online) leave(cl *client) {
	o.mu.Lock()
	delete(o.clients[cl.room], cl)
	if len(o.clients[cl.room]) == 0 {
		delete(o.clients, cl.room)
	}
	o.hangUpLocked(cl)
	o.mu.Unlock()
	if cl.player != "" {
		cl.room.Disconnect(cl.player, o.now())
		o.broadcast(cl.room)
	}
}

func (o *online) enqueue(cl *client, msg []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.enqueueLocked(cl, msg)
}

func (o *online) enqueueLocked(cl *client, msg []byte) {
	if cl.closed {
		return
	}
	select {
	case cl.send <- msg:
	default:
		o.hangUpLocked(cl) // too slow to keep up; it can reconnect
	}
}

func (o *online) hangUpLocked(cl *client) {
	if !cl.closed {
		cl.closed = true
		close(cl.send)
	}
}

// writeLoop sends queued messages and keepalive pings until the client is
// hung up, then closes the socket (which also ends readLoop).
func (cl *client) writeLoop() {
	ping := time.NewTicker(wsPingEvery)
	defer ping.Stop()
	for {
		select {
		case msg, ok := <-cl.send:
			if !ok {
				_ = cl.ws.Close(websocket.StatusNormalClosure, "")
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), wsWriteTimeout)
			err := cl.ws.Write(ctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				cl.ws.CloseNow()
				return
			}
		case <-ping.C:
			ctx, cancel := context.WithTimeout(context.Background(), wsWriteTimeout)
			err := cl.ws.Ping(ctx)
			cancel()
			if err != nil {
				cl.ws.CloseNow()
				return
			}
		}
	}
}

// allow is a token bucket: msgRate messages a second, bursts up to msgBurst.
func (cl *client) allow(now time.Time) bool {
	cl.tokens = min(msgBurst, cl.tokens+now.Sub(cl.last).Seconds()*msgRate)
	cl.last = now
	if cl.tokens < 1 {
		return false
	}
	cl.tokens--
	return true
}

// startSweeper runs the once-a-second room upkeep (round deadlines, held
// seats, host handover, expiry) while any room is open.
func (o *online) startSweeper() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sweeping || o.closed {
		return
	}
	o.sweeping = true
	go o.sweep()
}

func (o *online) sweep() {
	t := time.NewTicker(o.tick)
	defer t.Stop()
	for range t.C {
		now := o.now()
		changed, closed := o.hub.Sweep(now)
		for _, r := range changed {
			o.broadcast(r)
		}
		o.mu.Lock()
		for r, cls := range o.clients {
			for _, code := range closed {
				if r.Code() == code {
					for cl := range cls {
						o.enqueueLocked(cl, errorMsg(rooms.Error("room_closed")))
						o.hangUpLocked(cl)
					}
				}
			}
		}
		o.creates.prune(now)
		o.joins.prune(now)
		if o.hub.Len() == 0 || o.closed {
			o.sweeping = false
			o.mu.Unlock()
			return
		}
		o.mu.Unlock()
	}
}

// Close tells every connected player the server is restarting and hangs
// up. Rooms are in memory, so a deploy ends them; the page offers a rejoin.
func (o *online) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = true
	for _, cls := range o.clients {
		for cl := range cls {
			o.enqueueLocked(cl, errorMsg(errServerRestarting))
			o.hangUpLocked(cl)
		}
	}
}

// qrSVG serves GET /r/:code/qr.svg: a QR code of the room's join link.
// Dark modules on light, with the standard 4-module quiet zone, scan most
// reliably.
func (o *online) qrSVG(c *gin.Context) {
	code := rooms.NormalizeCode(c.Param("code"))
	if !roomCodeRe.MatchString(code) {
		c.String(http.StatusNotFound, "not found")
		return
	}
	q, err := qr.Encode(o.baseURL+"/r/"+code, qr.M)
	if err != nil {
		c.String(http.StatusInternalServerError, "internal error")
		return
	}
	const quiet = 4
	n := q.Size + 2*quiet
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 `)
	b.WriteString(strconv.Itoa(n) + " " + strconv.Itoa(n))
	b.WriteString(`" shape-rendering="crispEdges"><rect width="100%" height="100%" fill="#ebf3ee"/><path fill="#040e08" d="`)
	for y := range q.Size {
		for x := range q.Size {
			if q.Black(x, y) {
				b.WriteString("M" + strconv.Itoa(x+quiet) + " " + strconv.Itoa(y+quiet) + "h1v1h-1z")
			}
		}
	}
	b.WriteString(`"/></svg>`)
	c.Header("Cache-Control", "public, max-age=86400")
	c.Data(http.StatusOK, "image/svg+xml", []byte(b.String()))
}

func errorCode(err error) string {
	var re rooms.Error
	if errors.As(err, &re) {
		return string(re)
	}
	return "internal_error"
}

func errorMsg(err error) []byte { return mustJSON(gin.H{"t": "error", "code": errorCode(err)}) }

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // only fixed, marshalable types are passed in
	}
	return b
}

// ipLimiter allows `limit` events per IP per window (fixed windows).
type ipLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	seen   map[string]ipWindow
}

type ipWindow struct {
	start time.Time
	n     int
}

func newIPLimiter(limit int, window time.Duration) *ipLimiter {
	return &ipLimiter{limit: limit, window: window, seen: make(map[string]ipWindow)}
}

func (l *ipLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.seen[ip]
	if now.Sub(w.start) >= l.window {
		w = ipWindow{start: now}
	}
	w.n++
	l.seen[ip] = w
	return w.n <= l.limit
}

func (l *ipLimiter) prune(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for ip, w := range l.seen {
		if now.Sub(w.start) >= l.window {
			delete(l.seen, ip)
		}
	}
}
