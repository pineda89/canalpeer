package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sockPath(t *testing.T) string {
	dir, _ := os.MkdirTemp("", "cpi") // short: Unix socket names are limited
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "i.sock")
}

func TestFirstInstanceIsPrimary(t *testing.T) {
	_, primary, err := claimInstance(sockPath(t))
	if err != nil || !primary {
		t.Fatalf("primary %v err %v", primary, err)
	}
}

func TestSecondInstanceHandsOverURL(t *testing.T) {
	path := sockPath(t)
	links, _, _ := claimInstance(path)
	if _, primary, err := claimInstance(path); err != nil || primary {
		t.Fatalf("second: primary %v err %v", primary, err)
	}
	if err := sendToPrimary(path, link); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-links:
		if got != link {
			t.Fatalf("got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("link not handed over")
	}
}

func TestStaleSocketIsReclaimed(t *testing.T) {
	path := sockPath(t)
	os.WriteFile(path, nil, 0o600) // left behind by a crashed instance
	if _, primary, err := claimInstance(path); err != nil || !primary {
		t.Fatalf("primary %v err %v", primary, err)
	}
}

// Review: instances starting together (a double-clicked link) must agree on one window.
func TestSimultaneousStartsHaveOnePrimary(t *testing.T) {
	for round := 0; round < 20; round++ {
		path := sockPath(t)
		results := make(chan bool, 8)
		for i := 0; i < 8; i++ {
			go func() {
				_, primary, _ := claimInstance(path)
				results <- primary
			}()
		}
		n := 0
		for i := 0; i < 8; i++ {
			if <-results {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("round %d: %d primaries", round, n)
		}
	}
}
