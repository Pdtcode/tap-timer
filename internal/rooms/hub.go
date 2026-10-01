package rooms

import (
	"math/rand/v2"
	"strings"
	"sync"
	"time"
)

// Code alphabet: no 0/O, 1/I/L, so codes survive being read aloud or off a
// screen. 31^4 is about 920k codes.
const (
	codeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
	codeLen      = 4
	// MaxRooms caps open rooms; creating more returns ErrBusy.
	MaxRooms = 1000
)

// Hub holds every open room in memory. Rooms don't survive a restart.
type Hub struct {
	mu       sync.Mutex
	rooms    map[string]*Room
	maxRooms int
	intn     func(n int) int
}

func NewHub() *Hub { return newHub(MaxRooms, rand.IntN) }

func newHub(maxRooms int, intn func(int) int) *Hub {
	return &Hub{rooms: make(map[string]*Room), maxRooms: maxRooms, intn: intn}
}

// Create opens a new, empty room with an unused code.
func (h *Hub) Create(now time.Time) (*Room, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.rooms) >= h.maxRooms {
		return nil, ErrBusy
	}
	for range 50 {
		code := h.newCode()
		if _, taken := h.rooms[code]; !taken {
			r := newRoom(code, now, h.intn)
			h.rooms[code] = r
			return r, nil
		}
	}
	return nil, ErrBusy // practically unreachable below MaxRooms
}

// Get finds a room by code, ignoring case and surrounding spaces.
func (h *Hub) Get(code string) (*Room, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r, ok := h.rooms[NormalizeCode(code)]; ok {
		return r, nil
	}
	return nil, ErrRoomNotFound
}

// Len reports how many rooms are open.
func (h *Hub) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.rooms)
}

// Sweep ticks every room and closes expired ones. It returns the rooms
// whose state changed (to broadcast) and the codes of rooms it closed.
// Call it about once a second.
func (h *Hub) Sweep(now time.Time) (changed []*Room, closed []string) {
	h.mu.Lock()
	rooms := make([]*Room, 0, len(h.rooms))
	for _, r := range h.rooms {
		rooms = append(rooms, r)
	}
	h.mu.Unlock()

	for _, r := range rooms {
		if r.Expired(now) {
			closed = append(closed, r.code)
			continue
		}
		if r.Tick(now) {
			changed = append(changed, r)
		}
	}
	if len(closed) > 0 {
		h.mu.Lock()
		for _, code := range closed {
			delete(h.rooms, code)
		}
		h.mu.Unlock()
	}
	return changed, closed
}

func (h *Hub) newCode() string {
	b := make([]byte, codeLen)
	for i := range b {
		b[i] = codeAlphabet[h.intn(len(codeAlphabet))]
	}
	return string(b)
}

// NormalizeCode upper-cases a code and trims spaces, so "k7qx " finds K7QX.
func NormalizeCode(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }
