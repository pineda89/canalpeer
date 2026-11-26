package main

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

//go:embed input.conf
var inputConf []byte

//go:embed controls.lua
var controlsLua []byte

// mpvArgs: the window is ours (title, keys, no default bindings), and the
// stream is the core on this machine, so silence means the broadcast is late,
// never a dead link (Android lost its buffer to an 8 s read timeout).
// The controls over the video are our script (controls.lua); mpv's own are off.
func mpvArgs(ipcPath, inputConfPath, controlsPath string) []string {
	args := []string{
		"--idle=yes", "--force-window=yes", "--title=CanalPeer", "--keep-open=yes",
		"--no-input-default-bindings", "--input-conf=" + inputConfPath,
		"--input-ipc-server=" + ipcPath,
		"--network-timeout=300", "--stream-lavf-o=reconnect=1,reconnect_streamed=1",
		"--cache=yes", "--demuxer-max-bytes=64MiB", "--demuxer-readahead-secs=10",
		"--osd-font-size=40", "--osd-bar=no", "--volume-max=100",
		"--focus-on=all", // a new link brings the window to the front
		"--osc=no",
	}
	if runtime.GOOS == "linux" {
		// The desktop matches the window to canalpeer.desktop: our name and icon.
		// --gpu-sw: without GPU drivers (a VM, a bare install) mpv 0.41 rejects
		// Mesa's software renderer, falls back to X11 and aborts on an assertion.
		args = append(args, "--x11-name=canalpeer", "--wayland-app-id=canalpeer", "--gpu-sw=yes")
	}
	if controlsPath != "" {
		args = append(args, "--script="+controlsPath)
	}
	return args
}

type mpvEvent struct {
	Name     string
	Property string
	Data     any
	Args     []string
	Reason   string // end-file: "eof", "stop", "error", ...
}

type mpvReply struct {
	Error string
	Data  any
}

// mpv drives one mpv process over its JSON IPC.
type mpv struct {
	conn   net.Conn
	mu     sync.Mutex
	nextID int
	wait   map[int]chan mpvReply
	ev     chan mpvEvent
	obsID  int
}

func dialMpv(ipcPath string) (*mpv, error) {
	conn, err := dialIPC(ipcPath)
	if err != nil {
		return nil, err
	}
	m := &mpv{conn: conn, wait: map[int]chan mpvReply{}, ev: make(chan mpvEvent, 64)}
	go m.read()
	return m, nil
}

// startMpv launches mpv and connects to it; the IPC socket appears shortly after start.
func startMpv(ctx context.Context, bin string, args []string, ipcPath string) (*mpv, *exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		m, err := dialMpv(ipcPath)
		if err == nil {
			return m, cmd, nil
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			return nil, nil, fmt.Errorf("mpv IPC: %w", err)
		}
	}
}

func (m *mpv) read() {
	defer close(m.ev)
	sc := bufio.NewScanner(m.conn)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var msg struct {
			RequestID *int     `json:"request_id"`
			Error     string   `json:"error"`
			Data      any      `json:"data"`
			Event     string   `json:"event"`
			Name      string   `json:"name"`
			Args      []string `json:"args"`
			Reason    string   `json:"reason"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		if msg.Event != "" {
			select {
			case m.ev <- mpvEvent{Name: msg.Event, Property: msg.Name, Data: msg.Data, Args: msg.Args, Reason: msg.Reason}:
			default: // a stuck consumer must not stall replies; the player reads state it relies on with get
			}
			continue
		}
		if msg.RequestID != nil {
			m.mu.Lock()
			ch := m.wait[*msg.RequestID]
			delete(m.wait, *msg.RequestID)
			m.mu.Unlock()
			if ch != nil {
				ch <- mpvReply{msg.Error, msg.Data}
			}
		}
	}
	m.mu.Lock()
	for id, ch := range m.wait {
		close(ch)
		delete(m.wait, id)
	}
	m.mu.Unlock()
}

func (m *mpv) request(args ...any) (any, error) {
	m.mu.Lock()
	m.nextID++
	id := m.nextID
	ch := make(chan mpvReply, 1)
	m.wait[id] = ch
	m.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"command": args, "request_id": id})
	if _, err := m.conn.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return nil, errors.New("mpv closed")
		}
		if r.Error != "success" {
			return nil, fmt.Errorf("mpv %v: %s", args[0], r.Error)
		}
		return r.Data, nil
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("mpv %v: timeout", args[0])
	}
}

func (m *mpv) command(args ...any) error { _, err := m.request(args...); return err }

func (m *mpv) get(prop string) (any, error) { return m.request("get_property", prop) }

func (m *mpv) observe(prop string) error {
	m.mu.Lock()
	m.obsID++
	id := m.obsID
	m.mu.Unlock()
	return m.command("observe_property", id, prop)
}

func (m *mpv) events() <-chan mpvEvent { return m.ev }

func (m *mpv) showText(text string, ms int) error { return m.command("show-text", text, ms) }

func (m *mpv) close() error { return m.conn.Close() }
