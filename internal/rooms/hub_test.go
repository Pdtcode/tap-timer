package rooms

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestHubCreateAndGet(t *testing.T) {
	h := NewHub()
	seen := map[string]bool{}
	for range 200 {
		r, err := h.Create(t0)
		if err != nil {
			t.Fatal(err)
		}
		code := r.Code()
		if len(code) != codeLen || strings.Trim(code, codeAlphabet) != "" {
			t.Fatalf("bad code %q", code)
		}
		if seen[code] {
			t.Fatalf("duplicate code %q", code)
		}
		seen[code] = true
	}
	for code := range seen {
		got, err := h.Get(" " + strings.ToLower(code) + " ")
		if err != nil || got.Code() != code {
			t.Fatalf("Get(%q) = %v, %v", code, got, err)
		}
		break
	}
	if _, err := h.Get("ZZZZ0"); !errors.Is(err, ErrRoomNotFound) {
		t.Errorf("unknown code: err = %v", err)
	}
}

func TestHubRetriesTakenCodes(t *testing.T) {
	// The first two codes come out the same; the third is different.
	seq := []int{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1}
	i := 0
	h := newHub(10, func(int) int { v := seq[i%len(seq)]; i++; return v })
	a, _ := h.Create(t0)
	b, err := h.Create(t0)
	if err != nil || a.Code() == b.Code() {
		t.Fatalf("codes %q and %q, err %v", a.Code(), b.Code(), err)
	}
}

func TestHubLimit(t *testing.T) {
	h := newHub(2, NewHub().intn)
	for range 2 {
		if _, err := h.Create(t0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Create(t0); !errors.Is(err, ErrBusy) {
		t.Errorf("err = %v, want ErrBusy", err)
	}
}

func TestHubSweep(t *testing.T) {
	h := NewHub()
	idle, _ := h.Create(t0)
	idle.Join("A", "tok-a", t0)

	busy, _ := h.Create(t0)
	a, _ := busy.Join("A", "tok-a", t0)
	busy.Join("B", "tok-b", t0)
	busy.Start(a, at(IdleExpiry-time.Minute))

	// The busy room's round passes its deadline; the idle room expires.
	changed, closed := h.Sweep(at(IdleExpiry))
	if len(closed) != 1 || closed[0] != idle.Code() {
		t.Errorf("closed = %v, want [%s]", closed, idle.Code())
	}
	if len(changed) != 1 || changed[0] != busy {
		t.Errorf("changed = %v, want the busy room", changed)
	}
	if h.Len() != 1 {
		t.Errorf("%d rooms left, want 1", h.Len())
	}
	if _, err := h.Get(idle.Code()); !errors.Is(err, ErrRoomNotFound) {
		t.Error("expired room still reachable")
	}
}
