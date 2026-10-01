// Package rooms holds the game logic for Online rooms: friends on their own
// phones play Goal Challenge together over several rounds.
//
// Each phone times its own taps and reports the result, so the server never
// measures time. A Room only opens rounds, collects results, decides when to
// reveal them and keeps score. It's a plain state machine: every method takes
// the current time, and Tick handles deadlines, which keeps it easy to test.
// Rooms are safe for concurrent use.
package rooms

import (
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	MinPlayers = 2
	MaxPlayers = 8
	NameMax    = 16 // characters, the same as Party mode

	// RoundSlack is how long players get on top of the goal before a round
	// closes without them.
	RoundSlack = 30 * time.Second
	// SeatHold is how long a disconnected player keeps their seat (and the
	// host role) before giving it up.
	SeatHold = 60 * time.Second
	// IdleExpiry closes a room nobody has used for this long.
	IdleExpiry = 30 * time.Minute

	maxElapsedCs = 9999 // 99.99s, the longest time the display can show
	// submitTolerance allows for clock differences when checking that a
	// result couldn't have been timed faster than the round has been open.
	submitTolerance = time.Second
)

// Goals are the goal times a host can pick, in seconds. Mixed (0) picks one
// of them at random for each round.
var Goals = []int{3, 5, 10, 15, 30}

// RoundOptions are the match lengths a host can pick.
var RoundOptions = []int{1, 3, 5, 10}

// Mixed as Settings.Goal means a random goal for each round.
const Mixed = 0

// Player colours, in seat order. The same palette as Party mode.
var colors = []string{"#ff4d4d", "#7c7cff", "#ff5fae", "#ffb627", "#56ccff", "#30fc60", "#ffe45c", "#b57bff"}

type Phase string

const (
	PhaseLobby    Phase = "lobby"     // joining and picking settings
	PhaseRound    Phase = "round"     // players are tapping
	PhaseReveal   Phase = "reveal"    // a round's results are shown
	PhaseMatchEnd Phase = "match_end" // the final round's results and standings
)

// Error is a failure a client can act on. Its text is the code sent to the
// client.
type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrRoomNotFound     Error = "room_not_found"
	ErrRoomFull         Error = "room_full"
	ErrMatchInProgress  Error = "match_in_progress"
	ErrNotHost          Error = "not_host"
	ErrWrongPhase       Error = "wrong_phase"
	ErrInvalidSettings  Error = "invalid_settings"
	ErrNotEnoughPlayers Error = "not_enough_players"
	ErrUnknownPlayer    Error = "unknown_player"
	ErrInvalidResult    Error = "invalid_result"
	ErrAlreadySubmitted Error = "already_submitted"
	ErrBusy             Error = "busy"
)

type Settings struct {
	Goal   int `json:"goal"`   // seconds, or Mixed
	Rounds int `json:"rounds"` // rounds per match
}

var DefaultSettings = Settings{Goal: 5, Rounds: 5}

func (s Settings) valid() bool {
	return (s.Goal == Mixed || slices.Contains(Goals, s.Goal)) && slices.Contains(RoundOptions, s.Rounds)
}

type player struct {
	id, name, color, token string
	joined                 time.Time
	connected              bool
	disconnectedAt         time.Time
	totalCs, wins          int // match score: total error in hundredths, rounds won
}

type result struct {
	elapsedCs int
	missed    bool
}

type Room struct {
	mu sync.Mutex

	code     string
	phase    Phase
	players  []*player // in join order
	host     string
	nextID   int
	settings Settings

	round    int
	goal     int // this round's goal, in seconds
	opened   time.Time
	deadline time.Time
	results  map[string]result // this round's results, by player ID
	reveal   []RevealEntry     // the last revealed round

	lastActive time.Time
	intn       func(n int) int // random source for mixed goals
}

func newRoom(code string, now time.Time, intn func(int) int) *Room {
	return &Room{code: code, phase: PhaseLobby, settings: DefaultSettings, lastActive: now, intn: intn}
}

// NewRoom creates an empty room. Hub.Create is the usual way to get one.
func NewRoom(code string, now time.Time) *Room { return newRoom(code, now, rand.IntN) }

func (r *Room) Code() string { return r.code }

// Join seats a new player, or gives a returning player (same token) their
// seat back. The first player to join becomes the host. New players can
// only join in the lobby or between matches.
func (r *Room) Join(name, token string, now time.Time) (id string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastActive = now

	if p := r.byToken(token); p != nil {
		p.connected = true
		p.disconnectedAt = time.Time{}
		if n := cleanName(name); n != "" {
			p.name = n
		}
		return p.id, nil
	}
	if r.phase == PhaseRound || r.phase == PhaseReveal {
		return "", ErrMatchInProgress
	}
	if len(r.players) >= MaxPlayers {
		return "", ErrRoomFull
	}
	r.nextID++
	p := &player{
		id:        "p" + strconv.Itoa(r.nextID),
		name:      cleanName(name),
		color:     r.freeColor(),
		token:     token,
		joined:    now,
		connected: true,
	}
	if p.name == "" {
		p.name = "Player " + strconv.Itoa(len(r.players)+1)
	}
	r.players = append(r.players, p)
	if r.host == "" {
		r.host = p.id
	}
	return p.id, nil
}

// Disconnect marks a player's connection as lost. They keep their seat for
// SeatHold, and can come back with Join and the same token.
func (r *Room) Disconnect(id string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.byID(id); p != nil && p.connected {
		p.connected = false
		p.disconnectedAt = now
	}
}

// UpdateSettings changes the goal and number of rounds (host, lobby only).
func (r *Room) UpdateSettings(by string, s Settings, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.hostIn(by, PhaseLobby, PhaseMatchEnd); err != nil {
		return err
	}
	if !s.valid() {
		return ErrInvalidSettings
	}
	r.lastActive = now
	r.settings = s
	return nil
}

// Start begins a match from the lobby, or opens the next round after a
// reveal (host only).
func (r *Room) Start(by string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.hostIn(by, PhaseLobby, PhaseReveal); err != nil {
		return err
	}
	if r.phase == PhaseLobby {
		if r.connectedCount() < MinPlayers {
			return ErrNotEnoughPlayers
		}
		r.dropDisconnected()
		for _, p := range r.players {
			p.totalCs, p.wins = 0, 0
		}
		r.round = 0
		r.reveal = nil
	}
	r.lastActive = now
	r.openRound(now)
	return nil
}

// Submit records a player's time for the current round. When every
// connected player has submitted, the round is revealed straight away.
func (r *Room) Submit(id string, round int, elapsed float64, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phase != PhaseRound || round != r.round {
		return ErrWrongPhase
	}
	if r.byID(id) == nil {
		return ErrUnknownPlayer
	}
	if _, done := r.results[id]; done {
		return ErrAlreadySubmitted
	}
	cs := int(elapsed*100 + 0.5)
	// A time can't be longer than the round has been open (with some slack
	// for clock differences); this catches stale or made-up results.
	if elapsed <= 0 || cs > maxElapsedCs || now.Sub(r.opened)+submitTolerance < time.Duration(cs)*10*time.Millisecond {
		return ErrInvalidResult
	}
	r.lastActive = now
	r.results[id] = result{elapsedCs: cs}
	if r.allConnectedDone() {
		r.revealRound()
	}
	return nil
}

// Rematch goes back to the lobby after a match, keeping the players and
// settings (host only).
func (r *Room) Rematch(by string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.hostIn(by, PhaseMatchEnd); err != nil {
		return err
	}
	r.lastActive = now
	r.phase = PhaseLobby
	r.round = 0
	r.reveal = nil
	r.dropDisconnected()
	for _, p := range r.players {
		p.totalCs, p.wins = 0, 0
	}
	return nil
}

// Tick applies everything that happens with time: closing a round at its
// deadline, freeing seats held too long and handing over the host role.
// It reports whether anything changed, so the caller knows to broadcast.
func (r *Room) Tick(now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := false
	if r.phase == PhaseRound && !now.Before(r.deadline) {
		r.revealRound()
		changed = true
	}
	// Seats are only given up between matches; mid-match, a missing player
	// just misses rounds.
	if r.phase == PhaseLobby || r.phase == PhaseMatchEnd {
		before := len(r.players)
		r.players = slices.DeleteFunc(r.players, func(p *player) bool {
			return !p.connected && now.Sub(p.disconnectedAt) >= SeatHold
		})
		changed = changed || len(r.players) != before
	}
	if h := r.byID(r.host); h == nil || (!h.connected && now.Sub(h.disconnectedAt) >= SeatHold) {
		if next := r.firstConnected(); next != nil && next.id != r.host {
			r.host = next.id
			changed = true
		} else if h == nil && next == nil && r.host != "" {
			r.host = ""
			changed = true
		}
	}
	return changed
}

// Expired reports whether the room should be closed: idle too long, or
// nobody left in it.
func (r *Room) Expired(now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return now.Sub(r.lastActive) >= IdleExpiry || (len(r.players) == 0 && now.Sub(r.lastActive) >= SeatHold)
}

func (r *Room) openRound(now time.Time) {
	r.round++
	r.goal = r.settings.Goal
	if r.goal == Mixed {
		r.goal = Goals[r.intn(len(Goals))]
	}
	r.results = make(map[string]result, len(r.players))
	r.opened = now
	r.deadline = now.Add(time.Duration(r.goal)*time.Second + RoundSlack)
	r.phase = PhaseRound
}

// revealRound scores the round. Anyone without a result counts as stopping
// at 0.00s, so their error equals the goal.
func (r *Room) revealRound() {
	goalCs := r.goal * 100
	r.reveal = make([]RevealEntry, 0, len(r.players))
	best := -1
	for _, p := range r.players {
		res, ok := r.results[p.id]
		if !ok {
			res = result{missed: true}
		}
		diff := res.elapsedCs - goalCs
		p.totalCs += abs(diff)
		if best < 0 || abs(diff) < best {
			best = abs(diff)
		}
		r.reveal = append(r.reveal, RevealEntry{
			Player:  p.id,
			Elapsed: float64(res.elapsedCs) / 100,
			Diff:    float64(diff) / 100,
			Missed:  res.missed,
			diffCs:  diff,
		})
	}
	for i := range r.reveal {
		if abs(r.reveal[i].diffCs) == best && !r.reveal[i].Missed {
			r.reveal[i].Won = true
			r.byID(r.reveal[i].Player).wins++
		}
	}
	slices.SortStableFunc(r.reveal, func(a, b RevealEntry) int { return abs(a.diffCs) - abs(b.diffCs) })
	if r.round >= r.settings.Rounds {
		r.phase = PhaseMatchEnd
	} else {
		r.phase = PhaseReveal
	}
}

func (r *Room) hostIn(by string, phases ...Phase) error {
	if r.byID(by) == nil {
		return ErrUnknownPlayer
	}
	if by != r.host {
		return ErrNotHost
	}
	if !slices.Contains(phases, r.phase) {
		return ErrWrongPhase
	}
	return nil
}

func (r *Room) allConnectedDone() bool {
	for _, p := range r.players {
		if _, done := r.results[p.id]; p.connected && !done {
			return false
		}
	}
	return true
}

func (r *Room) dropDisconnected() {
	r.players = slices.DeleteFunc(r.players, func(p *player) bool { return !p.connected })
}

func (r *Room) connectedCount() int {
	n := 0
	for _, p := range r.players {
		if p.connected {
			n++
		}
	}
	return n
}

func (r *Room) firstConnected() *player {
	for _, p := range r.players {
		if p.connected {
			return p
		}
	}
	return nil
}

func (r *Room) freeColor() string {
	for _, c := range colors {
		if !slices.ContainsFunc(r.players, func(p *player) bool { return p.color == c }) {
			return c
		}
	}
	return colors[0]
}

func (r *Room) byID(id string) *player {
	for _, p := range r.players {
		if p.id == id {
			return p
		}
	}
	return nil
}

func (r *Room) byToken(token string) *player {
	if token == "" {
		return nil
	}
	for _, p := range r.players {
		if p.token == token {
			return p
		}
	}
	return nil
}

// cleanName drops control and formatting characters, collapses whitespace
// and caps the length. Names are always rendered as text, never HTML.
func cleanName(s string) string {
	s = strings.Map(func(c rune) rune {
		if unicode.IsSpace(c) {
			return ' ' // tabs and line breaks become spaces, collapsed below
		}
		if unicode.IsControl(c) || unicode.In(c, unicode.Cf) {
			return -1
		}
		return c
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > NameMax {
		s = strings.TrimSpace(string(r[:NameMax]))
	}
	return s
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
