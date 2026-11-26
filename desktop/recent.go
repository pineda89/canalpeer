package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// recentRow is one played channel as the launcher shows it.
type recentRow struct {
	ID    string // the content id, played and copied as is
	Name  string // the channel's name, or "Unnamed · " and Short
	Short string // the id's ends, shown under the name
	Ago   string
}

// recentRows words the core's history (mobile.History) for the launcher.
func recentRows(historyJSON string, now time.Time) []recentRow {
	var entries []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		At   int64  `json:"at"`
	}
	if json.Unmarshal([]byte(historyJSON), &entries) != nil {
		return nil
	}
	var rows []recentRow
	for _, e := range entries {
		r := recentRow{ID: e.ID, Name: e.Name, Short: shortID(e.ID), Ago: agoText(now, time.Unix(e.At, 0))}
		if r.Name == "" {
			r.Name = "Unnamed · " + r.Short
		}
		rows = append(rows, r)
	}
	return rows
}

// agoText says how long ago, as the Android launcher does (Recent.kt).
func agoText(now, at time.Time) string {
	d := int64(now.Sub(at).Seconds())
	switch {
	case d < 60: // a clock moved back lands here too
		return "just now"
	case d < 3600:
		return fmt.Sprintf("%d min ago", d/60)
	case d < 86400:
		return fmt.Sprintf("%d h ago", d/3600)
	case d < 2*86400:
		return "yesterday"
	default:
		return fmt.Sprintf("%d days ago", d/86400)
	}
}

func shortID(id string) string {
	r := []rune(id)
	if len(r) <= 8 {
		return id
	}
	return string(r[:4]) + "…" + string(r[len(r)-4:])
}
