package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// startOnline runs the full app on a real listener (WebSockets need one),
// with a controllable clock.
func startOnline(t *testing.T, tweak func(*http.Server)) (*httptest.Server, *App, *fakeClock) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	app, err := NewApp(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	app.online.now = clock.Now
	ts := httptest.NewUnstartedServer(app.Handler)
	if tweak != nil {
		tweak(ts.Config)
	}
	ts.Start()
	t.Cleanup(ts.Close)
	return ts, app, clock
}

func createRoom(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	res, err := http.Post(ts.URL+"/api/rooms", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct{ Code, Error string }
	json.NewDecoder(res.Body).Decode(&body)
	if res.StatusCode != http.StatusCreated || len(body.Code) != 4 {
		t.Fatalf("create room: %d %+v", res.StatusCode, body)
	}
	return body.Code
}

type wsClient struct {
	t  *testing.T
	ws *websocket.Conn
}

func dial(t *testing.T, ts *httptest.Server, code string) *wsClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws/"+code, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	return &wsClient{t: t, ws: ws}
}

func (c *wsClient) send(v any) {
	c.t.Helper()
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageText, b); err != nil {
		c.t.Fatalf("send: %v", err)
	}
}

// next reads messages until one of type typ arrives (or anything matching
// "error" when waiting for something else fails the test).
func (c *wsClient) next(typ string) map[string]any {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			c.t.Fatalf("waiting for %q: %v", typ, err)
		}
		var m map[string]any
		json.Unmarshal(data, &m)
		if m["t"] == typ {
			return m
		}
		if m["t"] == "error" {
			c.t.Fatalf("waiting for %q, got error %v", typ, m["code"])
		}
	}
}

// state waits for a state message whose phase is phase.
func (c *wsClient) state(phase string) map[string]any {
	c.t.Helper()
	return c.stateWhere(func(s map[string]any) bool { return s["phase"] == phase })
}

// stateWhere waits for a state message matching ok.
func (c *wsClient) stateWhere(ok func(map[string]any) bool) map[string]any {
	c.t.Helper()
	for {
		s := c.next("state")["state"].(map[string]any)
		if ok(s) {
			return s
		}
	}
}

func (c *wsClient) join(name, token string) string {
	c.t.Helper()
	c.send(map[string]any{"t": "join", "name": name, "token": token})
	return c.next("welcome")["you"].(string)
}

const tokA, tokB = "token-aaaaaaaaaaaaaaaa", "token-bbbbbbbbbbbbbbbb"

func TestOnlineMatch(t *testing.T) {
	ts, _, clock := startOnline(t, nil)
	code := createRoom(t, ts)

	a, b := dial(t, ts, code), dial(t, ts, strings.ToLower(code))
	idA := a.join("Pat", tokA)
	idB := b.join("Sam", tokB)
	lobby := a.stateWhere(func(s map[string]any) bool { return len(s["players"].([]any)) == 2 })
	if lobby["host"] != idA || len(lobby["players"].([]any)) != 2 {
		t.Fatalf("lobby = %v", lobby)
	}

	a.send(map[string]any{"t": "settings", "goal": 3, "rounds": 1})
	b.send(map[string]any{"t": "start"})
	if e := b.next("error"); e["code"] != "not_host" {
		t.Fatalf("guest start: %v", e)
	}
	a.send(map[string]any{"t": "start"})
	round := b.state("round")
	if round["goal"] != 3.0 || round["round"] != 1.0 {
		t.Fatalf("round = %v", round)
	}

	clock.Add(4 * time.Second)
	a.send(map[string]any{"t": "result", "round": 1, "elapsed": 2.91})
	// Before the reveal, the other player only learns that A is done.
	for {
		m := b.next("state")
		raw, _ := json.Marshal(m)
		if strings.Contains(string(raw), "2.91") || strings.Contains(string(raw), "elapsed") {
			t.Fatalf("time leaked before reveal: %s", raw)
		}
		if s := m["state"].(map[string]any); s["phase"] == "round" && s["players"].([]any)[0].(map[string]any)["done"] == true {
			break
		}
	}
	b.send(map[string]any{"t": "result", "round": 1, "elapsed": 3.2})

	end := a.state("match_end")
	reveal := end["reveal"].([]any)
	first := reveal[0].(map[string]any)
	if first["player"] != idA || first["elapsed"] != 2.91 || first["won"] != true {
		t.Fatalf("reveal = %v", reveal)
	}
	if b.state("match_end")["reveal"].([]any)[1].(map[string]any)["player"] != idB {
		t.Fatal("B didn't get the same reveal")
	}
}

func TestOnlineRejoinKeepsSeat(t *testing.T) {
	ts, _, _ := startOnline(t, nil)
	code := createRoom(t, ts)
	a := dial(t, ts, code)
	id := a.join("Pat", tokA)
	a.ws.Close(websocket.StatusNormalClosure, "reload")

	again := dial(t, ts, code)
	if got := again.join("", tokA); got != id {
		t.Fatalf("rejoined as %s, want seat %s", got, id)
	}
	s := again.state("lobby")
	p := s["players"].([]any)[0].(map[string]any)
	if len(s["players"].([]any)) != 1 || p["name"] != "Pat" || p["connected"] != true {
		t.Fatalf("players = %v", s["players"])
	}
}

// The http.Server's read/write timeouts must not cut WebSockets off.
func TestOnlineSocketOutlivesServerTimeouts(t *testing.T) {
	ts, _, _ := startOnline(t, func(s *http.Server) {
		s.ReadTimeout = 200 * time.Millisecond
		s.WriteTimeout = 200 * time.Millisecond
	})
	code := createRoom(t, ts)
	c := dial(t, ts, code)
	time.Sleep(500 * time.Millisecond)
	c.join("Pat", tokA)
	time.Sleep(500 * time.Millisecond)
	c.send(map[string]any{"t": "settings", "goal": 10, "rounds": 3})
	c.stateWhere(func(s map[string]any) bool { return s["settings"].(map[string]any)["goal"] == 10.0 })
}

func TestOnlineErrors(t *testing.T) {
	ts, _, _ := startOnline(t, nil)

	if e := dial(t, ts, "ZZZZ").next("error"); e["code"] != "room_not_found" {
		t.Errorf("unknown room: %v", e)
	}

	code := createRoom(t, ts)
	c := dial(t, ts, code)
	c.send(map[string]any{"t": "start"})
	if e := c.next("error"); e["code"] != "not_joined" {
		t.Errorf("command before join: %v", e)
	}
	c.send(map[string]any{"t": "join", "name": "Pat", "token": "short"})
	if e := c.next("error"); e["code"] != "bad_token" {
		t.Errorf("bad token: %v", e)
	}
	c.ws.Write(context.Background(), websocket.MessageText, []byte("{not json"))
	if e := c.next("error"); e["code"] != "bad_message" {
		t.Errorf("bad json: %v", e)
	}

	// Another site can't open a socket on a visitor's behalf.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, res, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws/"+code,
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://evil.example"}}})
	if err == nil || res == nil || res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin socket: err %v, res %v", err, res)
	}
}

func TestOnlineMessageRateLimit(t *testing.T) {
	ts, _, _ := startOnline(t, nil)
	c := dial(t, ts, createRoom(t, ts))
	c.join("Pat", tokA)
	for range msgBurst + 5 {
		c.send(map[string]any{"t": "settings", "goal": 5, "rounds": 3})
	}
	if e := c.next("error"); e["code"] != "rate_limited" {
		t.Errorf("flood: %v", e)
	}
}

func TestOnlineCreateRateLimit(t *testing.T) {
	ts, _, _ := startOnline(t, nil)
	for range createsPerMinute {
		createRoom(t, ts)
	}
	res, err := http.Post(ts.URL+"/api/rooms", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", res.StatusCode)
	}
}

func TestOnlineShutdownNotifiesPlayers(t *testing.T) {
	ts, app, _ := startOnline(t, nil)
	c := dial(t, ts, createRoom(t, ts))
	c.join("Pat", tokA)
	c.state("lobby")
	app.Close()
	if e := c.next("error"); e["code"] != "server_restarting" {
		t.Errorf("got %v", e)
	}
}

func TestOnlineSweeperClosesRounds(t *testing.T) {
	ts, app, clock := startOnline(t, nil)
	app.online.tick = 20 * time.Millisecond
	code := createRoom(t, ts) // starts the sweeper... with the old tick
	app.online.mu.Lock()
	app.online.sweeping = false // restart it with the fast tick
	app.online.mu.Unlock()
	app.online.startSweeper()

	a, b := dial(t, ts, code), dial(t, ts, code)
	a.join("Pat", tokA)
	b.join("Sam", tokB)
	a.send(map[string]any{"t": "settings", "goal": 3, "rounds": 3})
	a.send(map[string]any{"t": "start"})
	a.state("round")
	clock.Add(33 * time.Second) // goal 3s + 30s slack
	s := b.state("reveal")
	if r := s["reveal"].([]any); len(r) != 2 || r[0].(map[string]any)["missed"] != true {
		t.Fatalf("reveal = %v", r)
	}
}

func TestOnlinePages(t *testing.T) {
	r := newTestRouter(t, testConfig())

	w := get(r, "/online")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "data-online") || strings.Contains(w.Body.String(), `name="robots"`) {
		t.Errorf("/online: %d", w.Code)
	}
	w = get(r, "/r/k7qx")
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `data-room="K7QX"`) || !strings.Contains(body, `<meta name="robots" content="noindex">`) || w.Header().Get("X-Robots-Tag") != "noindex" {
		t.Errorf("/r/k7qx: %d", w.Code)
	}
	if w := get(r, "/r/K0QX"); w.Code != http.StatusNotFound {
		t.Errorf("/r/K0QX (0 isn't in the alphabet): %d", w.Code)
	}
	w = get(r, "/r/K7QX/qr.svg")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/svg+xml" || !strings.HasPrefix(w.Body.String(), "<svg") {
		t.Errorf("qr: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if w := get(r, "/r/nope/qr.svg"); w.Code != http.StatusNotFound {
		t.Errorf("bad qr code: %d", w.Code)
	}
	if !strings.Contains(get(r, "/sitemap.xml").Body.String(), "https://taptimer.test/online") {
		t.Error("/online missing from sitemap")
	}
}
