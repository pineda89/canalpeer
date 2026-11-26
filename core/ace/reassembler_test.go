package ace

import (
	"bytes"
	"testing"
)

// The tiny synthetic piece from wire.py's demo: 2 chunks of 4 bytes, 2-byte sig.
func TestReassemblerPythonDemo(t *testing.T) {
	r := NewReassembler(8, 4, 2)
	if got, _ := r.Add(5, 0, []byte("AB")); got != nil {
		t.Fatal("incomplete piece returned bytes")
	}
	if got, _ := r.Add(5, 1, []byte("CD")); string(got) != "AB" {
		t.Fatalf("completed piece = %q, want sig-stripped %q", got, "AB")
	}
	if got := r.Drain(0); len(got) != 1 || string(got[0]) != "AB" {
		t.Fatalf("drain = %q", got)
	}
}

func TestReassemblerEmitsInOrder(t *testing.T) {
	r := NewReassembler(4, 2, 0)
	r.SetStart(10)
	r.Add(11, 0, []byte("cc"))
	r.Add(11, 1, []byte("dd"))
	if got := r.Drain(0); len(got) != 0 {
		t.Fatalf("piece 11 drained before 10: %q", got)
	}
	r.Add(10, 1, []byte("bb"))
	r.Add(10, 0, []byte("aa"))
	got := bytes.Join(r.Drain(0), nil)
	if string(got) != "aabbccdd" {
		t.Fatalf("got %q", got)
	}
}

func TestReassemblerDropsBelowFront(t *testing.T) {
	r := NewReassembler(4, 2, 0)
	r.SetStart(100)
	if r.Add(99, 0, []byte("xx")); r.Pending() != 0 {
		t.Fatal("kept a chunk from below the front")
	}
	r.Add(100, 0, []byte("aa"))
	r.Add(100, 1, []byte("bb"))
	r.Drain(0)
	// A late peer pushes its older window: it must not rewind.
	r.Add(100, 0, []byte("zz"))
	r.Add(100, 1, []byte("zz"))
	if got := r.Drain(0); len(got) != 0 {
		t.Fatalf("rewound: %q", got)
	}
	if front, _ := r.Front(); front != 101 {
		t.Fatalf("front %d", front)
	}
}

func TestReassemblerSetStartOnlyOnce(t *testing.T) {
	r := NewReassembler(4, 2, 0)
	r.SetStart(5)
	r.SetStart(50)
	if front, _ := r.Front(); front != 5 {
		t.Fatalf("front %d", front)
	}
}

func TestReassemblerFirstChunkPinsStart(t *testing.T) {
	r := NewReassembler(4, 2, 0)
	r.Add(42, 1, []byte("bb"))
	if front, ok := r.Front(); !ok || front != 42 {
		t.Fatalf("front %d %v", front, ok)
	}
}

func TestReassemblerSkipAfter(t *testing.T) {
	r := NewReassembler(2, 1, 0)
	r.SetStart(0)
	r.Add(0, 0, []byte("a"))
	r.Add(0, 1, []byte("a"))
	r.Add(1, 0, []byte("x")) // piece 1 never completes
	for p := int64(2); p <= 3; p++ {
		r.Add(p, 0, []byte("b"))
		r.Add(p, 1, []byte("b"))
	}
	if got := bytes.Join(r.Drain(3), nil); string(got) != "aa" {
		t.Fatalf("skipped too early: %q", got)
	}
	r.Add(4, 0, []byte("c"))
	r.Add(4, 1, []byte("c"))
	if got := bytes.Join(r.Drain(3), nil); string(got) != "bbbbcc" {
		t.Fatalf("after skip: %q", got)
	}
	if r.Pending() != 0 {
		t.Fatal("the abandoned partial piece is still held")
	}
}

func TestReassemblerNoSkipWhenDisabled(t *testing.T) {
	r := NewReassembler(2, 1, 0)
	r.SetStart(0)
	for p := int64(1); p <= 40; p++ {
		r.Add(p, 0, []byte("b"))
		r.Add(p, 1, []byte("b"))
	}
	if got := r.Drain(0); len(got) != 0 {
		t.Fatal("skipped with skipAfter=0")
	}
}

func TestReassemblerResyncDropsStale(t *testing.T) {
	r := NewReassembler(2, 1, 0)
	r.SetStart(0)
	r.Add(1, 0, []byte("x"))
	r.Add(2, 0, []byte("y"))
	r.Add(2, 1, []byte("y"))
	r.Add(9, 0, []byte("z"))
	r.ResyncTo(5)
	if front, _ := r.Front(); front != 5 {
		t.Fatalf("front %d", front)
	}
	if r.Pending() != 1 || r.Complete(2) {
		t.Fatalf("stale pieces kept: pending=%d", r.Pending())
	}
	r.ResyncTo(3) // backwards is ignored
	if front, _ := r.Front(); front != 5 {
		t.Fatalf("resync went backwards to %d", front)
	}
}

func TestReassemblerIgnoresOutOfRangeChunk(t *testing.T) {
	r := NewReassembler(4, 2, 0)
	r.Add(1, 0, []byte("aa"))
	if got, _ := r.Add(1, 7, []byte("zz")); got != nil {
		t.Fatal("an out-of-range chunk index completed a piece")
	}
	if got, _ := r.Add(1, -1, []byte("zz")); got != nil {
		t.Fatal("a negative chunk index completed a piece")
	}
}

func TestReassemblerDuplicateChunkDoesNotComplete(t *testing.T) {
	r := NewReassembler(6, 2, 0)
	r.Add(1, 0, []byte("aa"))
	r.Add(1, 0, []byte("aa"))
	if got, _ := r.Add(1, 1, []byte("bb")); got != nil {
		t.Fatal("completed with 2 of 3 chunks")
	}
}
