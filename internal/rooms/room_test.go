package rooms

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return t0.Add(d) }

// fixed returns a random source that always picks index i.
func fixed(i int) func(int) int { return func(int) int { return i } }

func newTestRoom() *Room { return newRoom("K7QX", t0, fixed(0)) }

func join(t *testing.T, r *Room, name string) string {
	t.Helper()
	id, err := r.Join(name, "tok-"+name, t0)
	if err != nil {
		t.Fatalf("Join(%q): %v", name, err)
	}
	return id
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func seat(s State, id string) PlayerState {
	for _, p := range s.Players {
		if p.ID == id {
			return p
		}
	}
	return PlayerState{}
}

func TestJoin(t *testing.T) {
	r := newTestRoom()
	a := join(t, r, "Pat")
	b := join(t, r, "  Sam \t Lee  ")
	c := join(t, r, "")
	d := join(t, r, "a\u0000b​c this name is far too long")

	s := r.Snapshot(t0)
	if s.Host != a {
		t.Errorf("host = %q, want first player %q", s.Host, a)
	}
	for id, want := range map[string]string{a: "Pat", b: "Sam Lee", c: "Player 3", d: "abc this name is"} {
		if got := seat(s, id).Name; got != want {
			t.Errorf("name of %s = %q, want %q", id, got, want)
		}
	}
	if seat(s, a).Color == seat(s, b).Color {
		t.Error("players share a colour")
	}
}

func TestJoinLimits(t *testing.T) {
	r := newTestRoom()
	ids := make([]string, 0, MaxPlayers)
	for i := range MaxPlayers {
		ids = append(ids, join(t, r, string(rune('A'+i))))
	}
	_, err := r.Join("Late", "tok-late", t0)
	wantErr(t, err, ErrRoomFull)

	// A returning player gets their seat back even when the room is full.
	r.Disconnect(ids[3], at(time.Second))
	id, err := r.Join("", "tok-D", at(2*time.Second))
	must(t, err)
	if id != ids[3] || !seat(r.Snapshot(t0), id).Connected || seat(r.Snapshot(t0), id).Name != "D" {
		t.Errorf("rejoin gave %q, want seat %q back, connected, same name", id, ids[3])
	}

	// No new players once a match is underway; returning ones are fine.
	r2 := newTestRoom()
	a, _ := join(t, r2, "A"), join(t, r2, "B")
	must(t, r2.Start(a, t0))
	_, err = r2.Join("C", "tok-C", t0)
	wantErr(t, err, ErrMatchInProgress)
	if _, err := r2.Join("B", "tok-B", t0); err != nil {
		t.Errorf("returning player during match: %v", err)
	}
}

func TestSettings(t *testing.T) {
	r := newTestRoom()
	a, b := join(t, r, "A"), join(t, r, "B")

	wantErr(t, r.UpdateSettings(b, Settings{Goal: 10, Rounds: 3}, t0), ErrNotHost)
	wantErr(t, r.UpdateSettings(a, Settings{Goal: 7, Rounds: 3}, t0), ErrInvalidSettings)
	wantErr(t, r.UpdateSettings(a, Settings{Goal: 10, Rounds: 4}, t0), ErrInvalidSettings)
	must(t, r.UpdateSettings(a, Settings{Goal: Mixed, Rounds: 10}, t0))
	if s := r.Snapshot(t0).Settings; s != (Settings{Goal: Mixed, Rounds: 10}) {
		t.Errorf("settings = %+v", s)
	}

	must(t, r.Start(a, t0))
	wantErr(t, r.UpdateSettings(a, Settings{Goal: 5, Rounds: 3}, t0), ErrWrongPhase)
}

func TestStartNeedsTwoPlayers(t *testing.T) {
	r := newTestRoom()
	a := join(t, r, "A")
	wantErr(t, r.Start(a, t0), ErrNotEnoughPlayers)
	b := join(t, r, "B")
	wantErr(t, r.Start(b, t0), ErrNotHost)
	r.Disconnect(b, t0)
	wantErr(t, r.Start(a, t0), ErrNotEnoughPlayers)
}

func TestFullMatch(t *testing.T) {
	r := newTestRoom()
	a, b, c := join(t, r, "A"), join(t, r, "B"), join(t, r, "C")
	must(t, r.UpdateSettings(a, Settings{Goal: 5, Rounds: 3}, t0))
	must(t, r.Start(a, t0))

	// Round 1, goal 5: A 4.90 (-0.10), B 5.20 (+0.20), C 5.10 (+0.10).
	s := r.Snapshot(at(time.Second))
	if s.Phase != PhaseRound || s.Round != 1 || s.Goal != 5 || s.ClosesIn != 34000 {
		t.Fatalf("round 1 state = %+v", s)
	}
	must(t, r.Submit(a, 1, 4.90, at(6*time.Second)))
	must(t, r.Submit(b, 1, 5.20, at(6*time.Second)))
	if r.Snapshot(t0).Phase != PhaseRound {
		t.Fatal("revealed before everyone submitted")
	}
	must(t, r.Submit(c, 1, 5.10, at(6*time.Second)))

	s = r.Snapshot(at(7 * time.Second))
	if s.Phase != PhaseReveal {
		t.Fatalf("phase = %s, want reveal once everyone submitted", s.Phase)
	}
	// Closest first; A and C tie on 0.10 off and both win the round.
	if got := s.Reveal; len(got) != 3 || got[0].Player != a || got[1].Player != c || got[2].Player != b ||
		!got[0].Won || !got[1].Won || got[2].Won || got[0].Diff != -0.10 || got[2].Diff != 0.20 {
		t.Fatalf("reveal = %+v", got)
	}
	if seat(s, a).Rank != 1 || seat(s, c).Rank != 1 || seat(s, b).Rank != 3 {
		t.Errorf("ranks after round 1: A=%d C=%d B=%d", seat(s, a).Rank, seat(s, c).Rank, seat(s, b).Rank)
	}

	// Only the host moves on, and only after a reveal.
	wantErr(t, r.Start(b, at(8*time.Second)), ErrNotHost)
	must(t, r.Start(a, at(8*time.Second)))
	// Round 2: A 5.50 (+0.50), B 5.00 (exact), C 4.80 (-0.20).
	must(t, r.Submit(a, 2, 5.50, at(14*time.Second)))
	must(t, r.Submit(b, 2, 5.00, at(14*time.Second)))
	must(t, r.Submit(c, 2, 4.80, at(14*time.Second)))
	must(t, r.Start(a, at(15*time.Second)))
	// Round 3: A 5.00, B 5.30 (+0.30), C 5.05 (+0.05).
	must(t, r.Submit(a, 3, 5.00, at(21*time.Second)))
	must(t, r.Submit(b, 3, 5.30, at(21*time.Second)))
	must(t, r.Submit(c, 3, 5.05, at(21*time.Second)))

	s = r.Snapshot(at(22 * time.Second))
	if s.Phase != PhaseMatchEnd {
		t.Fatalf("phase = %s, want match_end after the last round", s.Phase)
	}
	// Totals: A 0.10+0.50+0 = 0.60, B 0.20+0+0.30 = 0.50, C 0.10+0.20+0.05 = 0.35.
	for id, want := range map[string]struct {
		total      float64
		wins, rank int
	}{c: {0.35, 1, 1}, b: {0.50, 1, 2}, a: {0.60, 2, 3}} {
		p := seat(s, id)
		if p.Total != want.total || p.Wins != want.wins || p.Rank != want.rank {
			t.Errorf("%s: total %.2f wins %d rank %d, want %+v", p.Name, p.Total, p.Wins, p.Rank, want)
		}
	}
	wantErr(t, r.Start(a, at(23*time.Second)), ErrWrongPhase)
}

func TestTiesBrokenByRoundWins(t *testing.T) {
	r := newTestRoom()
	a, b := join(t, r, "A"), join(t, r, "B")
	must(t, r.UpdateSettings(a, Settings{Goal: 5, Rounds: 3}, t0))
	must(t, r.Start(a, t0))
	// Both end on 0.30 total; A wins two rounds, B one.
	for round, times := range [][2]float64{{5.05, 5.20}, {5.05, 5.10}, {5.20, 5.00}} {
		now := at(time.Duration(round*10+6) * time.Second)
		must(t, r.Submit(a, round+1, times[0], now))
		must(t, r.Submit(b, round+1, times[1], now))
		if round < 2 {
			must(t, r.Start(a, now))
		}
	}
	s := r.Snapshot(at(time.Minute))
	if pa, pb := seat(s, a), seat(s, b); pa.Total != pb.Total || pa.Rank != 1 || pb.Rank != 2 {
		t.Errorf("A total %.2f rank %d wins %d, B total %.2f rank %d wins %d", pa.Total, pa.Rank, pa.Wins, pb.Total, pb.Rank, pb.Wins)
	}
}

func TestMissedRound(t *testing.T) {
	r := newTestRoom()
	a, b := join(t, r, "A"), join(t, r, "B")
	must(t, r.UpdateSettings(a, Settings{Goal: 10, Rounds: 3}, t0))
	must(t, r.Start(a, t0))
	must(t, r.Submit(a, 1, 9.8, at(12*time.Second)))

	if r.Tick(at(39 * time.Second)) {
		t.Fatal("round closed before its deadline (goal + 30s)")
	}
	if !r.Tick(at(40 * time.Second)) {
		t.Fatal("round still open at its deadline")
	}
	s := r.Snapshot(at(40 * time.Second))
	missed := s.Reveal[1]
	if s.Phase != PhaseReveal || missed.Player != b || !missed.Missed || missed.Diff != -10 || missed.Won {
		t.Fatalf("reveal = %+v", s.Reveal)
	}
	if seat(s, b).Total != 10 {
		t.Errorf("missed round should cost the goal: total = %.2f", seat(s, b).Total)
	}
}

func TestDisconnectedPlayerDoesNotHoldUpRound(t *testing.T) {
	r := newTestRoom()
	a, b, c := join(t, r, "A"), join(t, r, "B"), join(t, r, "C")
	must(t, r.Start(a, t0))
	r.Disconnect(c, at(time.Second))
	must(t, r.Submit(a, 1, 5, at(6*time.Second)))
	must(t, r.Submit(b, 1, 5, at(6*time.Second)))
	if s := r.Snapshot(t0); s.Phase != PhaseReveal || !s.Reveal[2].Missed {
		t.Fatalf("want reveal with C missed, got %s %+v", s.Phase, s.Reveal)
	}
}

func TestSubmitValidation(t *testing.T) {
	r := newTestRoom()
	a, b := join(t, r, "A"), join(t, r, "B")
	wantErr(t, r.Submit(a, 1, 5, t0), ErrWrongPhase)
	must(t, r.Start(a, t0))

	for _, tc := range []struct {
		name    string
		id      string
		round   int
		elapsed float64
		now     time.Duration
		want    error
	}{
		{"wrong round", a, 2, 5, 6 * time.Second, ErrWrongPhase},
		{"unknown player", "p99", 1, 5, 6 * time.Second, ErrUnknownPlayer},
		{"zero", a, 1, 0, 6 * time.Second, ErrInvalidResult},
		{"negative", a, 1, -1, 6 * time.Second, ErrInvalidResult},
		{"over 99.99", a, 1, 100, 101 * time.Second, ErrInvalidResult},
		{"longer than the round has been open", a, 1, 5, 3 * time.Second, ErrInvalidResult},
	} {
		if err := r.Submit(tc.id, tc.round, tc.elapsed, at(tc.now)); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	// Within a second of tolerance for clock differences is fine.
	must(t, r.Submit(a, 1, 5, at(4200*time.Millisecond)))
	wantErr(t, r.Submit(a, 1, 5, at(6*time.Second)), ErrAlreadySubmitted)
	must(t, r.Submit(b, 1, 5.004, at(6*time.Second)))
	if got := r.Snapshot(t0).Reveal; got[0].Elapsed != 5 || got[1].Elapsed != 5 {
		t.Errorf("times not rounded to hundredths: %+v", got)
	}
}

func TestNoTimesBeforeReveal(t *testing.T) {
	r := newTestRoom()
	a, b := join(t, r, "A"), join(t, r, "B")
	must(t, r.Start(a, t0))
	must(t, r.Submit(a, 1, 4.87, at(6*time.Second)))

	s := r.Snapshot(at(6 * time.Second))
	if !seat(s, a).Done || seat(s, b).Done {
		t.Errorf("done flags: A %v, B %v", seat(s, a).Done, seat(s, b).Done)
	}
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), "4.87") || strings.Contains(string(raw), "elapsed") || strings.Contains(string(raw), "tok-") {
		t.Errorf("snapshot leaks a time or token before the reveal: %s", raw)
	}
}

func TestMixedGoals(t *testing.T) {
	picks := []int{4, 0, 2} // 30s, 3s, 10s
	i := 0
	r := newRoom("K7QX", t0, func(n int) int { p := picks[i%len(picks)]; i++; return p })
	a, b := join(t, r, "A"), join(t, r, "B")
	must(t, r.UpdateSettings(a, Settings{Goal: Mixed, Rounds: 3}, t0))
	now := t0
	for _, want := range []int{30, 3, 10} {
		must(t, r.Start(a, now))
		if g := r.Snapshot(now).Goal; g != want {
			t.Fatalf("goal = %d, want %d", g, want)
		}
		now = now.Add(time.Minute)
		must(t, r.Submit(a, r.Snapshot(now).Round, 1, now))
		must(t, r.Submit(b, r.Snapshot(now).Round, 1, now))
	}
}

func TestHostHandover(t *testing.T) {
	r := newTestRoom()
	a, b, c := join(t, r, "A"), join(t, r, "B"), join(t, r, "C")
	must(t, r.Start(a, t0))
	r.Disconnect(a, at(time.Second))
	r.Disconnect(b, at(time.Second))

	r.Tick(at(time.Second + SeatHold - time.Millisecond))
	if h := r.Snapshot(t0).Host; h != a {
		t.Fatalf("host moved before SeatHold: %s", h)
	}
	r.Tick(at(time.Second + SeatHold))
	if h := r.Snapshot(t0).Host; h != c {
		t.Fatalf("host = %s, want the earliest connected player %s", h, c)
	}
	// A comes back, but the role stays with C.
	_, err := r.Join("A", "tok-A", at(2*time.Minute))
	must(t, err)
	r.Tick(at(2 * time.Minute))
	if h := r.Snapshot(t0).Host; h != c {
		t.Errorf("host = %s after A returned, want %s to keep it", h, c)
	}
}

func TestSeatsFreedOnlyBetweenMatches(t *testing.T) {
	r := newTestRoom()
	a, b, c := join(t, r, "A"), join(t, r, "B"), join(t, r, "C")

	// Lobby: a seat is freed after SeatHold, unless the player comes back.
	r.Disconnect(b, t0)
	r.Disconnect(c, t0)
	_, err := r.Join("", "tok-C", at(30*time.Second))
	must(t, err)
	r.Tick(at(SeatHold))
	if n := len(r.Snapshot(t0).Players); n != 2 {
		t.Fatalf("%d players in lobby, want 2 (B's seat freed, C back)", n)
	}

	// Mid-match: a missing player keeps their seat and just misses rounds.
	must(t, r.Start(a, at(2*time.Minute)))
	r.Disconnect(c, at(2*time.Minute))
	r.Tick(at(2*time.Minute + SeatHold + time.Second))
	if n := len(r.Snapshot(t0).Players); n != 2 {
		t.Errorf("%d players mid-match, want 2", n)
	}
}

func TestRematch(t *testing.T) {
	r := newTestRoom()
	a, b, c := join(t, r, "A"), join(t, r, "B"), join(t, r, "C")
	must(t, r.UpdateSettings(a, Settings{Goal: 3, Rounds: 1}, t0))
	wantErr(t, r.Rematch(a, t0), ErrWrongPhase)
	must(t, r.Start(a, t0))
	r.Disconnect(c, at(time.Second))
	must(t, r.Submit(a, 1, 3.1, at(4*time.Second)))
	must(t, r.Submit(b, 1, 2.5, at(4*time.Second)))

	wantErr(t, r.Rematch(b, at(5*time.Second)), ErrNotHost)
	must(t, r.Rematch(a, at(5*time.Second)))
	s := r.Snapshot(at(5 * time.Second))
	if s.Phase != PhaseLobby || s.Round != 0 || s.Reveal != nil || len(s.Players) != 2 {
		t.Fatalf("after rematch: %+v", s)
	}
	if p := seat(s, a); p.Total != 0 || p.Wins != 0 || p.Rank != 0 {
		t.Errorf("scores not reset: %+v", p)
	}
	if s.Settings != (Settings{Goal: 3, Rounds: 1}) {
		t.Errorf("settings not kept: %+v", s.Settings)
	}
	// New players can join between matches.
	if _, err := r.Join("D", "tok-D", at(6*time.Second)); err != nil {
		t.Errorf("join after match: %v", err)
	}
}

func TestExpiry(t *testing.T) {
	r := newTestRoom()
	join(t, r, "A")
	if r.Expired(at(IdleExpiry - time.Second)) {
		t.Error("expired early")
	}
	if !r.Expired(at(IdleExpiry)) {
		t.Error("not expired after IdleExpiry")
	}
	empty := newTestRoom()
	if empty.Expired(at(time.Second)) || !empty.Expired(at(SeatHold)) {
		t.Error("an empty room should close after SeatHold")
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"Pat":                   "Pat",
		"   ":                   "",
		"<b>Pat</b>":            "<b>Pat</b>", // kept as text; the client never renders HTML
		"Zoë 🎉":                 "Zoë 🎉",
		"line\nbreak":           "line break",
		"exactly sixteen!":      "exactly sixteen!",
		"seventeen chars!!":     "seventeen chars!",
		"fifteen chars  x tail": "fifteen chars x",
	} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
}
