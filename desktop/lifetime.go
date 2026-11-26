package main

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// The core in this process feeds mpv: an mpv left behind keeps showing the
// buffer and then a frozen frame. Whatever ends the app ends every mpv first.
var exitHooks struct {
	sync.Mutex
	next  int
	stops map[int]func()
	once  sync.Once
}

// killOnExit runs stop if the app is told to end (a signal, or the Dock's Quit
// on macOS); release unregisters it once the child has ended on its own.
func killOnExit(stop func()) (release func()) {
	exitHooks.once.Do(func() {
		exitHooks.stops = map[int]func(){}
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		go func() {
			<-ch
			endApp()
		}()
	})
	exitHooks.Lock()
	defer exitHooks.Unlock()
	exitHooks.next++
	id := exitHooks.next
	exitHooks.stops[id] = stop
	return func() {
		exitHooks.Lock()
		delete(exitHooks.stops, id)
		exitHooks.Unlock()
	}
}

// endApp stops every registered child and exits.
func endApp() {
	exitHooks.Lock()
	for _, stop := range exitHooks.stops {
		stop()
	}
	exitHooks.Unlock()
	os.Exit(0)
}
