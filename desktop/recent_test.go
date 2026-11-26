package main

import (
	"testing"
	"time"
)

func TestAgoText(t *testing.T) {
	at := func(d time.Duration) string { return agoText(t0d, t0d.Add(-d)) }
	cases := []struct {
		got, want string
	}{
		{at(0), "just now"},
		{at(-1000 * time.Second), "just now"}, // a clock moved back
		{at(125 * time.Second), "2 min ago"},
		{at(7300 * time.Second), "2 h ago"},
		{at(90000 * time.Second), "yesterday"},
		{at(3*24*time.Hour + 5*time.Second), "3 days ago"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

var t0d = time.Unix(1_800_000_000, 0)

func TestRecentRows(t *testing.T) {
	js := `[{"id":"a1","name":"X","at":1799999880},{"id":"f16d68fa5ff64f3894a45062ca28e4e66f142c6a","name":"","at":1799999990}]`
	rows := recentRows(js, t0d)
	if len(rows) != 2 {
		t.Fatalf("rows %+v", rows)
	}
	if rows[0] != (recentRow{ID: "a1", Name: "X", Short: "a1", Ago: "2 min ago"}) {
		t.Errorf("row 0 %+v", rows[0])
	}
	if rows[1].Name != "Unnamed · f16d…2c6a" || rows[1].Short != "f16d…2c6a" || rows[1].Ago != "just now" {
		t.Errorf("row 1 %+v", rows[1])
	}
	if recentRows("nope", t0d) != nil {
		t.Error("malformed JSON gave rows")
	}
}
