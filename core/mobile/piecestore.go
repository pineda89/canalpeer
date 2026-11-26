package mobile

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// pieceStore keeps a movie as one file per piece: "<index>" once verified,
// "<index>.part" while it arrives. A finished piece survives restarts by
// existing; evicting it is deleting it. Past capacity, the pieces furthest
// behind the playhead go first, then the furthest ahead.
type pieceStore struct {
	dir      string
	capacity int64
	playhead func() int // piece index being watched
	evicted  func(int)  // tells the torrent a finished piece is gone

	mu      sync.Mutex
	done    map[int]int64 // finished pieces on disk and their sizes
	writing map[int]int64 // pieces with a .part, counted against capacity
}

func newPieceStore(dir string, capacity int64, playhead func() int, evicted func(int)) storage.ClientImpl {
	return &pieceStore{dir: dir, capacity: capacity, playhead: playhead, evicted: evicted,
		done: map[int]int64{}, writing: map[int]int64{}}
}

func (s *pieceStore) OpenTorrent(_ context.Context, info *metainfo.Info, _ metainfo.Hash) (storage.TorrentImpl, error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return storage.TorrentImpl{}, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return storage.TorrentImpl{}, err
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".part") {
			// The torrent forgot which chunks it wrote: it starts the piece over.
			os.Remove(filepath.Join(s.dir, name))
			continue
		}
		i, err := strconv.Atoi(name)
		fi, ferr := e.Info()
		if err != nil || ferr != nil || i < 0 || i >= info.NumPieces() || fi.Size() != info.Piece(i).Length() {
			os.Remove(filepath.Join(s.dir, name))
			continue
		}
		s.done[i] = fi.Size()
	}
	// No TorrentImpl.Capacity: with it, anacrolix v1.61's reader retries any
	// error by recursion, a cancelled read included, until the stack overflows.
	// Readers only ask for what is near them, so the eviction here is enough.
	return storage.TorrentImpl{
		Piece: func(p metainfo.Piece) storage.PieceImpl { return &storePiece{s, p.Index(), p.Length()} },
		Close: func() error { return nil },
	}, nil
}

func (s *pieceStore) path(i int) string { return filepath.Join(s.dir, strconv.Itoa(i)) }

// makeRoom evicts finished and half-written pieces until one more piece of
// size n fits, sparing the playhead's piece and the one about to be written.
func (s *pieceStore) makeRoom(writing int, n int64) []int {
	var used int64
	for _, v := range s.done {
		used += v
	}
	for _, v := range s.writing {
		used += v
	}
	if used+n <= s.capacity {
		return nil
	}
	head := s.playhead()
	var behind, ahead []int
	for i := range s.writing {
		if i != head && i != writing {
			if i < head {
				behind = append(behind, i)
			} else {
				ahead = append(ahead, i)
			}
		}
	}
	for i := range s.done {
		switch {
		case i == head || i == writing:
		case i < head:
			behind = append(behind, i)
		default:
			ahead = append(ahead, i)
		}
	}
	slices.Sort(behind)
	slices.Sort(ahead)
	slices.Reverse(ahead)
	var gone []int
	for _, i := range append(behind, ahead...) {
		if used+n <= s.capacity {
			break
		}
		if n, ok := s.writing[i]; ok {
			// Abandoned by a seek: the torrent rewrites it, and a hash miss
			// sorts out whatever it thought was there.
			if os.Remove(s.path(i)+".part") != nil {
				continue
			}
			used -= n
			delete(s.writing, i)
			continue
		}
		if os.Remove(s.path(i)) != nil {
			continue // in use (Windows): try the next one
		}
		used -= s.done[i]
		delete(s.done, i)
		gone = append(gone, i)
	}
	return gone
}

type storePiece struct {
	s      *pieceStore
	i      int
	length int64
}

func (p *storePiece) ReadAt(b []byte, off int64) (int, error) {
	f, err := os.Open(p.s.path(p.i))
	if err != nil {
		if f, err = os.Open(p.s.path(p.i) + ".part"); err != nil {
			return 0, err
		}
	}
	defer f.Close()
	n, err := f.ReadAt(b, off)
	if err == io.EOF && n == len(b) {
		err = nil
	}
	return n, err
}

func (p *storePiece) WriteAt(b []byte, off int64) (int, error) {
	s := p.s
	s.mu.Lock()
	var gone []int
	if _, ok := s.writing[p.i]; !ok {
		gone = s.makeRoom(p.i, p.length)
		s.writing[p.i] = p.length
	}
	s.mu.Unlock()
	// Outside the lock: the torrent asks Completion back.
	for _, i := range gone {
		s.evicted(i)
	}
	// ponytail: a file open per chunk write; keep handles open if it ever shows in a profile.
	f, err := os.OpenFile(s.path(p.i)+".part", os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := f.WriteAt(b, off)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return n, err
}

func (p *storePiece) MarkComplete() error {
	s := p.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Rename(s.path(p.i)+".part", s.path(p.i)); err != nil {
		return err
	}
	delete(s.writing, p.i)
	s.done[p.i] = p.length
	return nil
}

func (p *storePiece) MarkNotComplete() error {
	s := p.s
	s.mu.Lock()
	defer s.mu.Unlock()
	os.Remove(s.path(p.i) + ".part")
	delete(s.writing, p.i)
	delete(s.done, p.i)
	if err := os.Remove(s.path(p.i)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (p *storePiece) Completion() storage.Completion {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	return storage.Completion{Ok: true, Complete: p.s.done[p.i] == p.length}
}
