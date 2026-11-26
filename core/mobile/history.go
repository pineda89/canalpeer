package mobile

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// historyEntry is one channel this device played.
type historyEntry struct {
	ID   string `json:"id"`   // as Normalize returns it
	Name string `json:"name"` // "" when the core knew none; the apps word the fallback
	At   int64  `json:"at"`   // unix seconds of the last time it started playing
}

const (
	historyMax  = 20
	historyFile = "history.json"
)

// historyMu serializes every read-modify-write of the file: two sessions can
// start playing at nearly the same time (a channel switch on the desktop).
var historyMu sync.Mutex

// loadHistory returns the entries newest first. A missing or unreadable file
// is an empty history: the list is a convenience, never an error to show.
func loadHistory(dir string) []historyEntry {
	historyMu.Lock()
	defer historyMu.Unlock()
	return readHistory(dir)
}

func readHistory(dir string) []historyEntry {
	h := []historyEntry{}
	b, err := os.ReadFile(filepath.Join(dir, historyFile))
	if err != nil || json.Unmarshal(b, &h) != nil {
		return []historyEntry{}
	}
	return h
}

// recordHistory puts id at the top, dropping its older entry and anything
// past historyMax.
func recordHistory(dir, id, name string, at time.Time) error {
	historyMu.Lock()
	defer historyMu.Unlock()
	h := []historyEntry{{ID: id, Name: name, At: at.Unix()}}
	for _, e := range readHistory(dir) {
		if e.ID != id && len(h) < historyMax {
			h = append(h, e)
		}
	}
	return writeHistory(dir, h)
}

// forgetHistory removes id; an id that is not listed is not an error.
func forgetHistory(dir, id string) error {
	historyMu.Lock()
	defer historyMu.Unlock()
	old := readHistory(dir)
	h := old[:0:0]
	for _, e := range old {
		if e.ID != id {
			h = append(h, e)
		}
	}
	if len(h) == len(old) {
		return nil
	}
	return writeHistory(dir, h)
}

// writeHistory replaces the file atomically: a crash mid-write leaves the
// previous list, never a torn one.
func writeHistory(dir string, h []historyEntry) error {
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "history-*.tmp")
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, historyFile)); err != nil {
		os.Remove(f.Name())
		return err
	}
	return nil
}

// History returns the channels this device played, newest first, as a JSON
// array of {"id", "name", "at"} (unix seconds; name "" when unknown).
func History(dataDir string) string {
	if dataDir == "" {
		return "[]"
	}
	b, err := json.Marshal(loadHistory(dataDir))
	if err != nil {
		return "[]"
	}
	return string(b)
}

// Forget removes a channel from the history.
func Forget(dataDir, id string) {
	if dataDir == "" {
		return
	}
	if err := forgetHistory(dataDir, id); err != nil {
		log.Print("history: ", err)
	}
}
