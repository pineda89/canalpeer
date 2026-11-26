package mobile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"

	"canalpeer.com/core/ace"
)

// movie is the file a movie session serves.
type movie struct {
	file     *torrent.File
	pieceLen int64
	head     *atomic.Int64 // piece index last read: the store evicts around it
}

// serve answers ranges from the torrent; reads past what arrived wait for it,
// and the read position steers what downloads next.
func (m *movie) serve(w http.ResponseWriter, req *http.Request) {
	r := m.file.NewReader()
	defer r.Close()
	r.SetContext(req.Context())
	r.SetResponsive()
	r.SetReadahead(16 << 20)
	// Without a type ServeContent sniffs, reading the first piece before every
	// range; Android has no mime table to name it from.
	w.Header().Set("Content-Type", videoTypes[strings.ToLower(path.Ext(m.file.DisplayPath()))])
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	http.ServeContent(w, req, path.Base(m.file.DisplayPath()), time.Time{}, &headReader{r, m, 0})
}

var videoTypes = map[string]string{
	".mkv": "video/x-matroska", ".mp4": "video/mp4", ".m4v": "video/mp4", ".avi": "video/x-msvideo",
	".webm": "video/webm", ".ts": "video/mp2t", ".m2ts": "video/mp2t", ".mov": "video/quicktime",
}

type headReader struct {
	torrent.Reader
	m   *movie
	pos int64
}

func (h *headReader) at(pos int64) {
	h.pos = pos
	h.m.head.Store((h.m.file.Offset() + pos) / h.m.pieceLen)
}

func (h *headReader) Read(b []byte) (int, error) {
	n, err := h.Reader.Read(b)
	h.at(h.pos + int64(n))
	return n, err
}

func (h *headReader) Seek(off int64, whence int) (int64, error) {
	pos, err := h.Reader.Seek(off, whence)
	if err == nil {
		h.at(pos)
	}
	return pos, err
}

// runMovie plays a movie: the largest file of a plain torrent, kept in the
// self-managed cache under cacheDir/vod. A non-nil warp carries all of it.
func (s *Session) runMovie(ctx context.Context, raw []byte, warp ace.Dialer) {
	s.mu.Lock()
	s.vod = true
	s.mu.Unlock()
	fail := func(code string, err error) {
		log.Print("movie: ", err)
		s.set("failed", code)
	}
	mi, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		fail("not_found", err)
		return
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		fail("not_found", err)
		return
	}
	if info.Name != "" {
		s.mu.Lock()
		s.name = info.Name
		s.mu.Unlock()
	}
	cacheDir := s.d.cacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(os.TempDir(), "canalpeer")
	}
	var size int64 // the largest file is all that downloads
	for _, f := range info.UpvertedFiles() {
		size = max(size, f.Length)
	}
	dir, capacity, err := prepareCache(filepath.Join(cacheDir, "vod"), mi.HashInfoBytes().HexString(), size, freeBytes, time.Now())
	if errors.Is(err, errNoSpace) {
		fail("no_space", err)
		return
	} else if err != nil {
		fail("network", err)
		return
	}

	var head atomic.Int64
	var tor atomic.Pointer[torrent.Torrent]
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dir
	cfg.ListenPort = 0
	// anacrolix logs every read a player cancels (each seek) as an error; the
	// session reports what matters itself.
	cfg.Slogger = slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg.DefaultStorage = newPieceStore(dir, capacity, func() int { return int(head.Load()) }, func(i int) {
		if t := tor.Load(); t != nil {
			t.Piece(i).UpdateCompletion()
		}
	})
	if warp != nil {
		// WARP takes no incoming connections and the client's own sockets
		// would go direct: no listener at all (so no uTP, DHT or incoming),
		// no port forwarding, no WebRTC; trackers, web seeds and peers dial
		// through warp. Uploading rides the outgoing connections.
		cfg.DisableTCP, cfg.DisableUTP, cfg.NoDHT = true, true, true
		cfg.AcceptPeerConnections, cfg.NoDefaultPortForwarding, cfg.DisableWebtorrent = false, true, true
		cfg.TrackerDialContext, cfg.HTTPDialContext = warp.DialContext, warp.DialContext
		cfg.TrackerListenPacket = func(string, string) (net.PacketConn, error) {
			return &dialedPacketConn{d: warp, ready: make(chan struct{})}, nil
		}
	}
	if s.d.clientConfig != nil {
		s.d.clientConfig(cfg)
	}
	c, err := torrent.NewClient(cfg)
	if err != nil {
		fail("network", err)
		return
	}
	defer c.Close()
	if warp != nil {
		c.AddDialer(torrent.NetworkDialer{Network: "tcp", Dialer: warp})
	}
	t, err := c.AddTorrent(mi)
	if err != nil {
		fail("not_found", err)
		return
	}
	tor.Store(t)
	// anacrolix stops downloading for good after a failed write unless told otherwise.
	t.SetOnWriteChunkError(func(err error) {
		if diskFull(err) {
			fail("no_space", err)
			return
		}
		log.Print("movie: write: ", err)
		t.AllowDataDownload()
	})
	if s.d.onTorrent != nil {
		s.d.onTorrent(t)
	}
	var file *torrent.File
	for _, f := range t.Files() {
		if file == nil || f.Length() > file.Length() {
			file = f
		}
	}
	s.mu.Lock()
	s.peers = func() int64 { return int64(t.Stats().ActivePeers) }
	s.mu.Unlock()
	s.set("searching", "")

	// Playing once the first bytes of the film are here.
	first := file.NewReader()
	first.SetResponsive()
	wait, cancel := context.WithTimeout(ctx, s.d.giveUpAfter)
	first.SetContext(wait)
	_, err = first.Read(make([]byte, 1))
	cancel()
	first.Close()
	switch {
	case ctx.Err() != nil:
		return
	case err != nil:
		fail("no_peers", err)
		return
	}
	s.movie.Store(&movie{file: file, pieceLen: info.PieceLength, head: &head})
	s.set("playing", "")
	<-ctx.Done()
}

// dialedPacketConn is the PacketConn a UDP tracker client asks for, made of a
// UDP "connection" dialed through d on the first write. Each tracker gets its
// own (anacrolix opens one per tracker), so every write goes to that address.
// ponytail: anacrolix resolves the tracker's name with the system resolver
// before writing; only the lookup goes direct, never the announce.
type dialedPacketConn struct {
	d     ace.Dialer
	once  sync.Once
	ready chan struct{} // closed once dialed, or closed
	c     net.Conn
	err   error
}

func (p *dialedPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	p.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		p.c, p.err = p.d.DialContext(ctx, "udp", addr.String())
		close(p.ready)
	})
	<-p.ready
	if p.err != nil {
		return 0, p.err
	}
	return p.c.Write(b)
}

func (p *dialedPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	<-p.ready
	if p.err != nil {
		return 0, nil, p.err
	}
	n, err := p.c.Read(b)
	return n, p.c.RemoteAddr(), err
}

func (p *dialedPacketConn) Close() error {
	p.once.Do(func() {
		p.err = net.ErrClosed
		close(p.ready)
	})
	<-p.ready
	if p.c != nil {
		return p.c.Close()
	}
	return nil
}

func (p *dialedPacketConn) LocalAddr() net.Addr { return &net.UDPAddr{} }
func (p *dialedPacketConn) SetDeadline(t time.Time) error {
	return p.ifDialed(func(c net.Conn) error { return c.SetDeadline(t) })
}
func (p *dialedPacketConn) SetReadDeadline(t time.Time) error {
	return p.ifDialed(func(c net.Conn) error { return c.SetReadDeadline(t) })
}
func (p *dialedPacketConn) SetWriteDeadline(t time.Time) error {
	return p.ifDialed(func(c net.Conn) error { return c.SetWriteDeadline(t) })
}

// ifDialed applies a deadline once there is a connection; before that it has
// nothing to apply to.
func (p *dialedPacketConn) ifDialed(f func(net.Conn) error) error {
	select {
	case <-p.ready:
		if p.c != nil {
			return f(p.c)
		}
	default:
	}
	return nil
}
