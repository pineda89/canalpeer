package main

/*
#cgo CFLAGS: -x objective-c -fmodules
#cgo LDFLAGS: -framework Cocoa
void runURLLoop(void);
*/
import "C"

import "runtime"

// macOS hands acestream:// links to an app as Apple Events, not arguments.
var urls = make(chan string, 8)

//export goURLEvent
func goURLEvent(u *C.char) { urls <- C.GoString(u) }

//export goTerminate
func goTerminate() { endApp() }

// Cocoa must own the main thread.
func init() { runtime.LockOSThread() }

func urlEvents() <-chan string { return urls }

// runMain runs app in a goroutine and gives the main thread to Cocoa.
func runMain(app func()) {
	go app()
	C.runURLLoop()
}
