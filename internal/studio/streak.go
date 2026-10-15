package studio

import "time"

const dayLayout = "2006-01-02"

// streak counts consecutive days with a post, ending today. A day you
// haven't posted yet doesn't break it: before your post today, it counts
// back from yesterday.
func streak(posted []time.Time, now time.Time, loc *time.Location) int {
	days := make(map[string]bool, len(posted))
	for _, t := range posted {
		days[t.In(loc).Format(dayLayout)] = true
	}
	d := now.In(loc)
	if !days[d.Format(dayLayout)] {
		d = d.AddDate(0, 0, -1)
	}
	n := 0
	for days[d.Format(dayLayout)] {
		n++
		d = d.AddDate(0, 0, -1)
	}
	return n
}

// calendarDay is one square in the dashboard's last-N-days strip.
type calendarDay struct {
	Date   time.Time
	Posted bool
	Today  bool
}

func calendar(posted []time.Time, now time.Time, loc *time.Location, n int) []calendarDay {
	days := make(map[string]bool, len(posted))
	for _, t := range posted {
		days[t.In(loc).Format(dayLayout)] = true
	}
	today := now.In(loc)
	out := make([]calendarDay, n)
	for i := range n {
		d := today.AddDate(0, 0, i-n+1)
		out[i] = calendarDay{Date: d, Posted: days[d.Format(dayLayout)], Today: i == n-1}
	}
	return out
}
