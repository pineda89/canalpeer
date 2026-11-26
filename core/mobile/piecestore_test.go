package mobile

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// testInfo is a 10-piece torrent of 10-byte pieces.
func testInfo() *metainfo.Info {
	return &metainfo.Info{Name: "m", PieceLength: 10, Length: 100, Pieces: make([]byte, 20*10)}
}

type evictLog struct {
	mu sync.Mutex
	n  []int
}

func (e *evictLog) add(i int) { e.mu.Lock(); e.n = append(e.n, i); e.mu.Unlock() }
func (e *evictLog) got() []int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Sorted(slices.Values(e.n))
}

func openStore(t *testing.T, dir string, capacity int64, playhead *int, ev *evictLog) (storage.TorrentImpl, *metainfo.Info) {
	t.Helper()
	info := testInfo()
	s := newPieceStore(dir, capacity, func() int { return *playhead }, ev.add)
	ti, err := s.OpenTorrent(context.Background(), info, metainfo.Hash{})
	if err != nil {
		t.Fatal(err)
	}
	return ti, info
}

func fill(t *testing.T, ti storage.TorrentImpl, info *metainfo.Info, i int) {
	t.Helper()
	p := ti.Piece(info.Piece(i))
	if _, err := p.WriteAt(bytes.Repeat([]byte{byte('a' + i)}, 10), 0); err != nil {
		t.Fatal(err)
	}
	if err := p.MarkComplete(); err != nil {
		t.Fatal(err)
	}
}

func TestPieceStoreSurvivesReopen(t *testing.T) {
	dir, head := t.TempDir(), 0
	ti, info := openStore(t, dir, 1000, &head, &evictLog{})
	fill(t, ti, info, 3)
	half := ti.Piece(info.Piece(4))
	half.WriteAt([]byte("xxxxx"), 0) // half written when the app quit
	if c := half.Completion(); !c.Ok || c.Complete {
		t.Fatalf("half piece reads as %+v", c)
	}
	ti.Close()

	ti, info = openStore(t, dir, 1000, &head, &evictLog{})
	if c := ti.Piece(info.Piece(3)).Completion(); !c.Ok || !c.Complete {
		t.Fatalf("finished piece lost: %+v", c)
	}
	if c := ti.Piece(info.Piece(4)).Completion(); c.Complete {
		t.Fatal("a stale .part counts as done")
	}
	buf := make([]byte, 4)
	if _, err := ti.Piece(info.Piece(3)).ReadAt(buf, 6); err != nil || string(buf) != "dddd" {
		t.Fatalf("read %q %v", buf, err)
	}
	ti.Piece(info.Piece(3)).MarkNotComplete()
	if _, err := os.Stat(filepath.Join(dir, "3")); !os.IsNotExist(err) {
		t.Fatal("MarkNotComplete left the piece on disk")
	}
}

func TestPieceStoreEvictsBehindThenFarAhead(t *testing.T) {
	dir, head := t.TempDir(), 0
	ev := &evictLog{}
	ti, info := openStore(t, dir, 40, &head, ev) // four pieces fit
	for _, i := range []int{1, 2, 6, 8} {
		fill(t, ti, info, i)
	}
	head = 5
	fill(t, ti, info, 5) // one must go: the furthest behind, piece 1
	if got := ev.got(); !slices.Equal(got, []int{1}) {
		t.Fatalf("evicted %v, want [1]", got)
	}
	head = 9
	fill(t, ti, info, 9) // behind 9: 2, 5, 6, 8 → 2 goes
	fill(t, ti, info, 0) // now 5 is the furthest behind among 5, 6, 8
	if got := ev.got(); !slices.Equal(got, []int{1, 2, 5}) {
		t.Fatalf("evicted %v, want [1 2 5]", got)
	}
	names, _ := os.ReadDir(dir)
	var left []string
	for _, n := range names {
		left = append(left, n.Name())
	}
	if strings.Join(left, ",") != "0,6,8,9" {
		t.Fatalf("on disk %v", left)
	}
}

func TestPieceStoreKeepsThePlayheadPiece(t *testing.T) {
	dir, head := t.TempDir(), 2
	ev := &evictLog{}
	ti, info := openStore(t, dir, 20, &head, ev) // two pieces fit
	fill(t, ti, info, 2)
	fill(t, ti, info, 3)
	fill(t, ti, info, 4) // only 3 may go: 2 is being watched
	if got := ev.got(); !slices.Equal(got, []int{3}) {
		t.Fatalf("evicted %v, want [3]", got)
	}
}

// Seeks leave half-written pieces nobody asks for again; they must not hold
// capacity forever.
func TestPieceStoreEvictsAbandonedParts(t *testing.T) {
	dir, head := t.TempDir(), 3
	ev := &evictLog{}
	ti, info := openStore(t, dir, 20, &head, ev)        // two pieces fit
	ti.Piece(info.Piece(1)).WriteAt([]byte("xxxxx"), 0) // abandoned after a seek
	fill(t, ti, info, 2)
	fill(t, ti, info, 3)
	if _, err := os.Stat(filepath.Join(dir, "1.part")); !os.IsNotExist(err) {
		t.Fatal("the abandoned part still holds room")
	}
	if c := ti.Piece(info.Piece(2)).Completion(); !c.Complete {
		t.Fatal("a finished piece went before the abandoned part")
	}
}
