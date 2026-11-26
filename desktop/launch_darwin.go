package main

/*
#cgo CFLAGS: -x objective-c -fmodules
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>
void cpShowLauncher(char *html);
void cpCloseLauncher(void);
*/
import "C"

import "sync"

// The one launcher window on screen, if any: Cocoa calls back into it.
var shown struct {
	sync.Mutex
	h      launcherHandlers
	closed chan struct{}
}

//export goLauncherMessage
func goLauncherMessage(fn, arg *C.char) *C.char {
	shown.Lock()
	h := shown.h
	shown.Unlock()
	reply := ""
	switch C.GoString(fn) {
	case "watch":
		reply = h.watch(C.GoString(arg))
	case "download":
		h.download()
	case "licenses":
		h.licenses()
	case "forget":
		h.forget(C.GoString(arg))
	case "setVpn":
		h.setVpn(C.GoString(arg))
	}
	return C.CString(reply) // freed by the caller
}

//export goLauncherClosed
func goLauncherClosed() {
	shown.Lock()
	defer shown.Unlock()
	if shown.closed != nil {
		close(shown.closed)
		shown.closed = nil
	}
}

// webViewOpener opens the launcher in a WKWebView window, which macOS 14+ always has.
func webViewOpener(string) windowOpener {
	return func(html string, h launcherHandlers) launcherWindow {
		closed := make(chan struct{})
		shown.Lock()
		shown.h, shown.closed = h, closed
		shown.Unlock()
		C.cpShowLauncher(C.CString(html)) // freed by the callee
		return launcherWindow{close: func() { C.cpCloseLauncher() }, closed: closed}
	}
}
