package rooms

import (
	"slices"
	"time"
)

// State is the full room snapshot sent to every player whenever anything
// changes. It's small (at most 8 players), and one message brings a
// reconnecting player fully up to date.
//
// Rule: until a round is revealed, nothing here contains anyone's time.
// Players only see who is Done.
type State struct {
	Code     string        `json:"code"`
	Phase    Phase         `json:"phase"`
	Host     string        `json:"host"`
	Players  []PlayerState `json:"players"` // in join order
	Settings Settings      `json:"settings"`
	Round    int           `json:"round"`              // 1-based; 0 in the lobby
	Goal     int           `json:"goal,omitempty"`     // this round's goal, in seconds
	ClosesIn int64         `json:"closesIn,omitempty"` // ms until the round closes (round phase)
	Reveal   []RevealEntry `json:"reveal,omitempty"`   // the last round, closest first (reveal and match_end)
}

type PlayerState struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Color     string  `json:"color"`
	Connected bool    `json:"connected"`
	Done      bool    `json:"done"`  // submitted this round
	Total     float64 `json:"total"` // match total error, in seconds
	Wins      int     `json:"wins"`  // rounds won this match
	Rank      int     `json:"rank"`  // match standing; 0 before the first reveal
}

type RevealEntry struct {
	Player  string  `json:"player"`
	Elapsed float64 `json:"elapsed"` // seconds; 0 when missed
	Diff    float64 `json:"diff"`    // elapsed - goal: + late, - early
	Missed  bool    `json:"missed,omitempty"`
	Won     bool    `json:"won,omitempty"` // closest this round (ties all win)

	diffCs int
}

// Snapshot returns the room as players see it.
func (r *Room) Snapshot(now time.Time) State {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := State{
		Code:     r.code,
		Phase:    r.phase,
		Host:     r.host,
		Settings: r.settings,
		Round:    r.round,
		Players:  make([]PlayerState, 0, len(r.players)),
	}
	if r.phase != PhaseLobby {
		s.Goal = r.goal
	}
	if r.phase == PhaseRound {
		s.ClosesIn = max(0, r.deadline.Sub(now).Milliseconds())
	}
	if r.phase == PhaseReveal || r.phase == PhaseMatchEnd {
		s.Reveal = slices.Clone(r.reveal)
	}
	ranks := r.ranks()
	for _, p := range r.players {
		_, done := r.results[p.id]
		s.Players = append(s.Players, PlayerState{
			ID:        p.id,
			Name:      p.name,
			Color:     p.color,
			Connected: p.connected,
			Done:      r.phase == PhaseRound && done,
			Total:     float64(p.totalCs) / 100,
			Wins:      p.wins,
			Rank:      ranks[p.id],
		})
	}
	return s
}

// ranks orders players by lowest total error, then most round wins.
// Players level on both share a rank.
func (r *Room) ranks() map[string]int {
	if r.round == 0 || (r.phase == PhaseRound && r.round == 1) {
		return nil // nothing scored yet
	}
	order := slices.Clone(r.players)
	cmp := func(a, b *player) int {
		if a.totalCs != b.totalCs {
			return a.totalCs - b.totalCs
		}
		return b.wins - a.wins
	}
	slices.SortStableFunc(order, cmp)
	ranks := make(map[string]int, len(order))
	for i, p := range order {
		if i > 0 && cmp(order[i-1], p) == 0 {
			ranks[p.id] = ranks[order[i-1].id]
		} else {
			ranks[p.id] = i + 1
		}
	}
	return ranks
}
