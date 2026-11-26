// Package changelog is what changed in each version, in plain words for viewers
// with little experience.
// changelog.json is the only copy: the apps show it after an update, the release
// scripts put it on GitHub (English) and the site shows it (Spanish and English).
package changelog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed changelog.json
var data []byte

// Entry is one version, newest first in the file. Date is empty until released.
// Before 1.4.0 the desktop app had its own number.
type Entry struct {
	Version string   `json:"version"`
	Date    string   `json:"date"`
	Desktop string   `json:"desktop"`
	En      []string `json:"en"`
	Es      []string `json:"es"`
}

var entries = func() []Entry {
	var e []Entry
	if err := json.Unmarshal(data, &e); err != nil {
		panic("changelog.json: " + err.Error()) // the tests catch it before a build ships
	}
	return e
}()

func All() []Entry { return entries }

// Find is the entry for version, or a zero Entry.
func Find(version string) Entry {
	for _, e := range entries {
		if e.Version == version {
			return e
		}
	}
	return Entry{}
}

// Notes is what changed in version, in Spanish for "es" and English otherwise.
// An item that starts with "Android" is only for platform "android"; one that
// starts with "Windows" or "Mac", only for "desktop"; "" (the site, GitHub) gets all.
func Notes(version, lang, platform string) []string {
	e := Find(version)
	items := e.En
	if lang == "es" {
		items = e.Es
	}
	if platform == "" {
		return items
	}
	var out []string
	for _, n := range items {
		android := strings.HasPrefix(n, "Android")
		desktop := strings.HasPrefix(n, "Windows") || strings.HasPrefix(n, "Mac")
		if (android && platform != "android") || (desktop && platform != "desktop") {
			continue
		}
		out = append(out, n)
	}
	return out
}

// Ready says whether version can be released: it has notes and a date.
func Ready(version string) error {
	e := Find(version)
	switch {
	case e.Version == "":
		return fmt.Errorf("changelog: no entry for %s in core/changelog/changelog.json", version)
	case e.Date == "":
		return fmt.Errorf("changelog: %s has no date; set it to the release day", version)
	}
	return nil
}

func heading(e Entry) string {
	h := e.Version
	if e.Desktop != "" {
		h += " · desktop " + e.Desktop
	}
	return fmt.Sprintf("%s (%s)", h, e.Date)
}

// Markdown is CHANGELOG.md for GitHub: released versions, newest first, in English.
func Markdown() string {
	var b strings.Builder
	b.WriteString("# What's new in CanalPeer\n")
	for _, e := range entries {
		if e.Date == "" {
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n\n", heading(e))
		for _, n := range e.En {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	return b.String()
}
