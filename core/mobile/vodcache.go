package mobile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const (
	cacheReserve = 1 << 30            // free space movies never take
	cacheMinimum = 64 << 20           // below this a movie cannot play smoothly
	cacheMaxAge  = 7 * 24 * time.Hour // movies unopened this long are deleted
)

var errNoSpace = errors.New("not enough free space for a movie")

// prepareCache readies root/id for a movie of size bytes and says how much of
// it may be on disk at once. Other movies go when unopened for cacheMaxAge,
// then least recently opened first while the movie would not fit; id's own
// folder is never touched.
func prepareCache(root, id string, size int64, free func(string) (int64, error), now time.Time) (string, int64, error) {
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	os.Chtimes(dir, now, now)
	type movie struct {
		path string
		at   time.Time
	}
	var others []movie
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		fi, err := e.Info()
		if !e.IsDir() || e.Name() == id || err != nil {
			continue
		}
		p := filepath.Join(root, e.Name())
		if now.Sub(fi.ModTime()) > cacheMaxAge {
			os.RemoveAll(p)
			continue
		}
		others = append(others, movie{p, fi.ModTime()})
	}
	slices.SortFunc(others, func(a, b movie) int { return a.at.Compare(b.at) })
	used := dirSize(dir)
	avail, err := free(root)
	if err != nil {
		return "", 0, err
	}
	for _, m := range others {
		if avail >= cacheReserve+size-used {
			break
		}
		os.RemoveAll(m.path)
		if avail, err = free(root); err != nil {
			return "", 0, err
		}
	}
	capacity := min(size, used+avail-cacheReserve)
	if capacity < min(size, cacheMinimum) {
		return "", 0, errNoSpace
	}
	return dir, capacity, nil
}

func dirSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}
