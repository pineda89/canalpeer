package mobile

import (
	"io"
	"testing"
	"time"
)

func readN(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	return string(buf)
}

func TestPipeDeliversInOrder(t *testing.T) {
	p := newPipe(1 << 10)
	r := p.reader(0)
	p.Write([]byte("ab"))
	p.Write([]byte("cd"))
	if got := readN(t, r, 4); got != "abcd" {
		t.Fatalf("got %q", got)
	}
}

func TestPipeDropsOldestOverCap(t *testing.T) {
	p := newPipe(4)
	p.Write([]byte("abc"))
	p.Write([]byte("def"))
	if got := readN(t, p.reader(0), 4); got != "cdef" { // keeps the newest cap bytes
		t.Fatalf("got %q", got)
	}
}

func TestPipeWriteCopiesInput(t *testing.T) {
	p := newPipe(1 << 10)
	b := []byte("xy")
	p.Write(b)
	b[0] = 'z'
	if got := readN(t, p.reader(0), 2); got != "xy" {
		t.Fatalf("got %q", got)
	}
}

func TestNewReaderGetsQueuedBytes(t *testing.T) {
	p := newPipe(1 << 10)
	p.Write([]byte("abc"))
	r1 := p.reader(0)
	if got := readN(t, r1, 1); got != "a" {
		t.Fatalf("r1 got %q", got)
	}
	r2 := p.reader(1) // the player reconnects asking for byte 1
	if _, err := r1.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("old reader: want EOF, got %v", err)
	}
	if got := readN(t, r2, 2); got != "bc" {
		t.Fatalf("r2 got %q", got)
	}
}

func TestReadBlocksUntilWrite(t *testing.T) {
	p := newPipe(1 << 10)
	r := p.reader(0)
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 8)
		n, _ := r.Read(buf)
		got <- string(buf[:n])
	}()
	select {
	case s := <-got:
		t.Fatalf("read returned %q before any write", s)
	case <-time.After(50 * time.Millisecond):
	}
	p.Write([]byte("x"))
	select {
	case s := <-got:
		if s != "x" {
			t.Fatalf("got %q", s)
		}
	case <-time.After(time.Second):
		t.Fatal("read did not wake up")
	}
}

func TestCloseUnblocksReader(t *testing.T) {
	p := newPipe(1 << 10)
	r := p.reader(0)
	errc := make(chan error, 1)
	go func() {
		_, err := r.Read(make([]byte, 8))
		errc <- err
	}()
	time.Sleep(20 * time.Millisecond)
	p.Close()
	select {
	case err := <-errc:
		if err != io.EOF {
			t.Fatalf("want EOF, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not unblock reader")
	}
}

func TestPipeSeekBackReplaysHistory(t *testing.T) {
	p := newPipe(1 << 10)
	p.Write([]byte("abcdef"))
	readN(t, p.reader(0), 6)
	// 6 bytes shown since the reader opened at 0; go back 4.
	if behind := p.seek(6, -4, 0); behind != 4 {
		t.Fatalf("behind %d, want 4", behind)
	}
	if got := readN(t, p.reader(0), 4); got != "cdef" {
		t.Fatalf("got %q", got)
	}
}

func TestPipeSeekClampsToWhatIsHeld(t *testing.T) {
	p := newPipe(4)
	p.Write([]byte("abc"))
	readN(t, p.reader(0), 3)
	p.Write([]byte("def")) // keeps "cdef": history counts against the cap
	if behind := p.seek(3, -100, 0); behind != 4 {
		t.Fatalf("back past history: behind %d, want 4", behind)
	}
	if behind := p.seek(0, +100, 0); behind != 0 || p.Len() != 0 {
		t.Fatalf("forward past live: behind %d len %d", behind, p.Len())
	}
}

func TestPipeSeekForwardKeepsAMargin(t *testing.T) {
	p := newPipe(1 << 10)
	p.Write([]byte("0123456789"))
	if behind := p.seek(2, +100, 3); behind != 3 {
		t.Fatalf("forward: behind %d, want the 3-byte margin", behind)
	}
	// Already inside the margin: forward does not move back.
	if behind := p.seek(2, +5, 3); behind != 1 {
		t.Fatalf("inside margin: behind %d, want 1", behind)
	}
}

func TestPipeSeekEndsTheCurrentReader(t *testing.T) {
	p := newPipe(1 << 10)
	p.Write([]byte("abc"))
	r := p.reader(0)
	p.seek(0, -1, 0)
	if _, err := r.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("old reader after seek: %v, want EOF", err)
	}
}

// A reconnecting player names the byte it wants; nothing it never got is lost.
func TestReaderResumesAtTheRequestedOffset(t *testing.T) {
	p := newPipe(1 << 10)
	p.Write([]byte("abcdefghij"))
	readN(t, p.reader(0), 8) // pushed 8, the player only kept 2
	if got := readN(t, p.reader(2), 3); got != "cde" {
		t.Fatalf("got %q", got)
	}
}

func TestReaderAfterSeekCountsFromTheTarget(t *testing.T) {
	p := newPipe(1 << 10)
	p.Write([]byte("abcdefghij"))
	readN(t, p.reader(0), 10)
	p.seek(10, -6, 0) // to "e"
	readN(t, p.reader(0), 4)
	if got := readN(t, p.reader(2), 3); got != "ghi" { // the sniff-then-reopen pattern
		t.Fatalf("got %q", got)
	}
}

// A stopped Session lingers until Java's GC frees it; its bytes must not.
func TestPipeCloseDropsHeldBytes(t *testing.T) {
	p := newPipe(1 << 10)
	r := p.reader(0)
	p.Write([]byte("abcd"))
	p.Close()
	if p.chunks != nil {
		t.Fatalf("still holding %d chunks", len(p.chunks))
	}
	if _, err := r.Read(make([]byte, 4)); err != io.EOF {
		t.Fatalf("read after close: %v", err)
	}
}
