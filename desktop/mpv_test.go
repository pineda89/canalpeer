package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeMpv speaks mpv's JSON IPC on a Unix socket.
type fakeMpv struct {
	t     *testing.T
	ln    net.Listener
	conn  chan net.Conn
	path  string
	lines chan map[string]any
}

func newFakeMpv(t *testing.T) *fakeMpv {
	dir, _ := os.MkdirTemp("", "cpmpv") // short path: Unix socket names are limited
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeMpv{t: t, ln: ln, conn: make(chan net.Conn, 1), path: path, lines: make(chan map[string]any, 16)}
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		f.conn <- c
		sc := bufio.NewScanner(c)
		for sc.Scan() {
			var m map[string]any
			json.Unmarshal(sc.Bytes(), &m)
			f.lines <- m
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeMpv) next() map[string]any {
	select {
	case m := <-f.lines:
		return m
	case <-time.After(2 * time.Second):
		f.t.Fatal("no command reached mpv")
		return nil
	}
}

func (f *fakeMpv) send(c net.Conn, v any) {
	b, _ := json.Marshal(v)
	c.Write(append(b, '\n'))
}

func TestCommandIsJSONLine(t *testing.T) {
	f := newFakeMpv(t)
	m, err := dialMpv(f.path)
	if err != nil {
		t.Fatal(err)
	}
	c := <-f.conn
	go func() {
		msg := f.next()
		cmd, _ := msg["command"].([]any)
		if !slices.Equal(cmd, []any{"loadfile", "http://x"}) || msg["request_id"] == nil {
			t.Errorf("got %v", msg)
		}
		f.send(c, map[string]any{"request_id": msg["request_id"], "error": "success"})
	}()
	if err := m.command("loadfile", "http://x"); err != nil {
		t.Fatal(err)
	}
}

func TestGetReturnsData(t *testing.T) {
	f := newFakeMpv(t)
	m, _ := dialMpv(f.path)
	c := <-f.conn
	go func() {
		msg := f.next()
		f.send(c, map[string]any{"request_id": msg["request_id"], "error": "success", "data": 12.5})
	}()
	v, err := m.get("time-pos")
	if err != nil || v != 12.5 {
		t.Fatalf("got %v %v", v, err)
	}
}

func TestEventsDeliverClientMessages(t *testing.T) {
	f := newFakeMpv(t)
	m, _ := dialMpv(f.path)
	c := <-f.conn
	f.send(c, map[string]any{"event": "client-message", "args": []string{"canalpeer", "back"}})
	select {
	case e := <-m.events():
		if e.Name != "client-message" || !slices.Equal(e.Args, []string{"canalpeer", "back"}) {
			t.Fatalf("got %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}
}

func TestEventsCloseWhenMpvGoes(t *testing.T) {
	f := newFakeMpv(t)
	m, _ := dialMpv(f.path)
	(<-f.conn).Close()
	select {
	case _, ok := <-m.events():
		if ok {
			t.Fatal("event after close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("events not closed")
	}
}

func TestMpvArgsNeverTimeOutOnLoopback(t *testing.T) {
	args := strings.Join(mpvArgs("/tmp/x.sock", "/tmp/input.conf", "/tmp/controls.lua"), " ")
	for _, want := range []string{"--network-timeout=300", "reconnect=1", "reconnect_streamed=1",
		"--input-ipc-server=/tmp/x.sock", "--input-conf=/tmp/input.conf", "--no-input-default-bindings", "--title=CanalPeer"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %s in %s", want, args)
		}
	}
}

func TestMpvArgsHasControls(t *testing.T) {
	args := strings.Join(mpvArgs("/tmp/x.sock", "/tmp/input.conf", "/tmp/controls.lua"), " ")
	for _, want := range []string{"--osc=no", "--script=/tmp/controls.lua"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %s in %s", want, args)
		}
	}
}

// Against the bundled mpv: CANALPEER_MPV=path/to/mpv go test -run TestRealMpv
func TestRealMpv(t *testing.T) {
	bin := os.Getenv("CANALPEER_MPV")
	if bin == "" {
		t.Skip("set CANALPEER_MPV to an mpv binary")
	}
	dir, _ := os.MkdirTemp("", "cpm")
	defer os.RemoveAll(dir)
	conf := filepath.Join(dir, "input.conf")
	os.WriteFile(conf, inputConf, 0o644)
	sock := filepath.Join(dir, "s")
	m, cmd, err := startMpv(t.Context(), bin, mpvArgs(sock, conf, ""), sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.showText("CanalPeer", 2000); err != nil {
		t.Fatal(err)
	}
	if v, err := m.get("idle-active"); err != nil || v != true {
		t.Fatalf("idle-active %v %v", v, err)
	}
	m.command("quit")
	select {
	case _, ok := <-m.events():
		for ok {
			_, ok = <-m.events()
		}
	case <-time.After(5 * time.Second):
		t.Fatal("events did not close after quit")
	}
	cmd.Wait()
}
