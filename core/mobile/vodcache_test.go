package mobile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeDisk reports base free bytes plus whatever was deleted from root since.
func fakeDisk(t *testing.T, root string, base int64) func(string) (int64, error) {
	start := dirSize(root)
	return func(string) (int64, error) { return base + start - dirSize(root), nil }
}

func movieDir(t *testing.T, root, id string, size int64, age time.Duration) {
	t.Helper()
	dir := filepath.Join(root, id)
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "0"), make([]byte, size), 0o600)
	at := time.Now().Add(-age)
	os.Chtimes(dir, at, at)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestPrepareCacheDropsStaleMovies(t *testing.T) {
	root := t.TempDir()
	movieDir(t, root, "old", 10, 8*24*time.Hour)
	movieDir(t, root, "recent", 10, time.Hour)
	dir, capacity, err := prepareCache(root, "new", 100<<20, fakeDisk(t, root, 50<<30), time.Now())
	if err != nil || dir != filepath.Join(root, "new") || capacity != 100<<20 {
		t.Fatalf("%s %d %v", dir, capacity, err)
	}
	if exists(filepath.Join(root, "old")) || !exists(filepath.Join(root, "recent")) {
		t.Fatal("only the movie unopened for 7 days goes")
	}
}

func TestPrepareCacheFreesLeastRecentFirst(t *testing.T) {
	root := t.TempDir()
	movieDir(t, root, "older", 200<<20, 3*time.Hour)
	movieDir(t, root, "newer", 200<<20, time.Hour)
	movieDir(t, root, "this", 50<<20, 5*time.Hour) // reopened: never deleted, its bytes count as room
	free := fakeDisk(t, root, cacheReserve+100<<20)
	// Wants 400 MiB: 50 are already here, 100 free past the reserve, 200 from
	// "older" makes 350 — still short, so "newer" goes too.
	_, capacity, err := prepareCache(root, "this", 400<<20, free, time.Now())
	if err != nil || capacity != 400<<20 {
		t.Fatalf("capacity %d %v", capacity, err)
	}
	if exists(filepath.Join(root, "older")) || exists(filepath.Join(root, "newer")) || !exists(filepath.Join(root, "this", "0")) {
		t.Fatal("wrong folders deleted")
	}
}

func TestPrepareCacheCapsToWhatFits(t *testing.T) {
	root := t.TempDir()
	_, capacity, err := prepareCache(root, "m", 4<<30, fakeDisk(t, root, cacheReserve+300<<20), time.Now())
	if err != nil || capacity != 300<<20 {
		t.Fatalf("capacity %d %v", capacity, err)
	}
	if _, _, err := prepareCache(root, "m", 4<<30, fakeDisk(t, root, cacheReserve+10<<20), time.Now()); !errors.Is(err, errNoSpace) {
		t.Fatalf("got %v, want errNoSpace", err)
	}
	// A short film smaller than the minimum still fits whole.
	if _, capacity, err := prepareCache(root, "short", 3<<20, fakeDisk(t, root, cacheReserve+10<<20), time.Now()); err != nil || capacity != 3<<20 {
		t.Fatalf("short film: %d %v", capacity, err)
	}
}
