package main

import (
	"strings"
	"testing"
)

// fakeWindow is a launcher window that the test drives like a viewer would.
type fakeWindow struct {
	html   string
	h      launcherHandlers
	closed chan struct{}
	byApp  bool
	opened chan struct{}
}

func (f *fakeWindow) open(html string, h launcherHandlers) launcherWindow {
	f.html, f.h = html, h
	close(f.opened)
	return launcherWindow{close: func() { f.byApp = true; close(f.closed) }, closed: f.closed}
}

func newFakeWindow() *fakeWindow {
	return &fakeWindow{closed: make(chan struct{}), opened: make(chan struct{})}
}

func launchAsync(f *fakeWindow, links chan string) chan [2]any {
	launch := makeLaunch(f.open, func() launcherView { return launcherView{Version: "1.1.0"} }, func() {}, func() {}, func(string) {}, func(string) {})
	res := make(chan [2]any, 1)
	go func() {
		l, q := launch(links)
		res <- [2]any{l, q}
	}()
	<-f.opened
	return res
}

func TestLaunchWatchValidLink(t *testing.T) {
	f := newFakeWindow()
	res := launchAsync(f, nil)
	id := strings.Repeat("cd", 20)
	if msg := f.h.watch(id); msg != "" {
		t.Fatalf("valid id refused: %q", msg)
	}
	r := <-res
	if r[0] != id || r[1] != false || !f.byApp {
		t.Fatalf("got %v, window closed by app %v", r, f.byApp)
	}
}

func TestLaunchBadLinkKeepsWindow(t *testing.T) {
	f := newFakeWindow()
	res := launchAsync(f, nil)
	if msg := f.h.watch("nonsense"); msg == "" {
		t.Fatal("bad link accepted")
	}
	select {
	case r := <-res:
		t.Fatalf("launcher ended on a bad link: %v", r)
	default:
	}
	close(f.closed) // the viewer closes it
	if r := <-res; r[1] != true {
		t.Fatalf("closing the launcher did not quit: %v", r)
	}
}

func TestLaunchLinkArrivesWhileOpen(t *testing.T) {
	f := newFakeWindow()
	links := make(chan string, 1)
	res := launchAsync(f, links)
	links <- "acestream://e"
	r := <-res
	if r[0] != "acestream://e" || r[1] != false || !f.byApp {
		t.Fatalf("got %v, window closed by app %v", r, f.byApp)
	}
}

func TestLaunchPageHasVersion(t *testing.T) {
	f := newFakeWindow()
	launchAsync(f, nil)
	if !strings.Contains(f.html, "Version 1.1.0") {
		t.Fatal("launcher page without the version")
	}
	close(f.closed)
}

func TestLaunchWindowFailureIsNotQuit(t *testing.T) {
	open := func(string, launcherHandlers) launcherWindow {
		closed := make(chan struct{})
		close(closed)
		return launcherWindow{close: func() {}, closed: closed, failed: true}
	}
	launch := makeLaunch(open, func() launcherView { return launcherView{} }, func() {}, func() {}, func(string) {}, func(string) {})
	if link, quit := launch(nil); link != "" || quit {
		t.Fatalf("a window that never opened reads as %q quit=%v", link, quit)
	}
}
