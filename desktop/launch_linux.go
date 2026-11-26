package main

import (
	"encoding/json"
	"log"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// The launcher on Linux is a GTK window with WebKitGTK, loaded at run time:
// without them (or without a display) the app still plays links, player window
// only. GTK wants one thread for everything, so the main thread runs its loop
// and the app hands it work through idle callbacks.

var gtk struct {
	ok bool

	initCheck         func(argc, argv unsafe.Pointer) bool
	main              func()
	idleAdd           func(fn, data uintptr) uint32
	signalConnect     func(instance uintptr, signal string, handler, data, destroy uintptr, flags int32) uint64
	windowNew         func(kind int32) uintptr
	windowSetTitle    func(w uintptr, title string)
	windowSetSize     func(w uintptr, width, height int32)
	windowSetPosition func(w uintptr, pos int32)
	windowSetIconName func(w uintptr, name string)
	windowPresent     func(w uintptr)
	containerAdd      func(container, child uintptr)
	widgetShowAll     func(w uintptr)
	widgetDestroy     func(w uintptr)
	managerNew        func() uintptr
	managerRegister   func(m uintptr, name string) bool
	managerAddScript  func(m, script uintptr)
	userScriptNew     func(src string, frames, when int32, allow, block uintptr) uintptr
	webViewNew        func(m uintptr) uintptr
	webViewLoadHTML   func(v uintptr, html string, base uintptr)
	webViewRunJS      func(v uintptr, js string, cancellable, cb, data uintptr)
	webViewBackground func(v uintptr, rgba *[4]float64)
	resultValue       func(result uintptr) uintptr
	jsValueString     func(value uintptr) string
}

// The page calls these like the WebView2 bindings on Windows.
const linuxBridge = `
window.cpPost = (fn, arg) => window.webkit.messageHandlers.cp.postMessage(JSON.stringify({fn, arg}));
window.cpWatch = t => new Promise(r => { window._cpResolve = r; cpPost('watch', t); });
window.cpDownload = () => cpPost('download', '');
window.cpLicenses = () => cpPost('licenses', '');
window.cpForget = id => cpPost('forget', id);
window.cpSetVpn = m => cpPost('setVpn', m);`

// GTK must start on the main thread, which runMain keeps.
func init() { runtime.LockOSThread() }

func loadGtk() (ok bool) {
	defer func() {
		if r := recover(); r != nil { // RegisterLibFunc panics on a missing symbol
			log.Print("launcher: ", r)
			ok = false
		}
	}()
	open := func(name string) uintptr {
		h, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			panic(err)
		}
		return h
	}
	glib, gobject := open("libglib-2.0.so.0"), open("libgobject-2.0.so.0")
	g, wk, jsc := open("libgtk-3.so.0"), open("libwebkit2gtk-4.1.so.0"), open("libjavascriptcoregtk-4.1.so.0")
	for _, f := range []struct {
		fn  any
		lib uintptr
		sym string
	}{
		{&gtk.initCheck, g, "gtk_init_check"},
		{&gtk.main, g, "gtk_main"},
		{&gtk.idleAdd, glib, "g_idle_add"},
		{&gtk.signalConnect, gobject, "g_signal_connect_data"},
		{&gtk.windowNew, g, "gtk_window_new"},
		{&gtk.windowSetTitle, g, "gtk_window_set_title"},
		{&gtk.windowSetSize, g, "gtk_window_set_default_size"},
		{&gtk.windowSetPosition, g, "gtk_window_set_position"},
		{&gtk.windowSetIconName, g, "gtk_window_set_icon_name"},
		{&gtk.windowPresent, g, "gtk_window_present"},
		{&gtk.containerAdd, g, "gtk_container_add"},
		{&gtk.widgetShowAll, g, "gtk_widget_show_all"},
		{&gtk.widgetDestroy, g, "gtk_widget_destroy"},
		{&gtk.managerNew, wk, "webkit_user_content_manager_new"},
		{&gtk.managerRegister, wk, "webkit_user_content_manager_register_script_message_handler"},
		{&gtk.managerAddScript, wk, "webkit_user_content_manager_add_script"},
		{&gtk.userScriptNew, wk, "webkit_user_script_new"},
		{&gtk.webViewNew, wk, "webkit_web_view_new_with_user_content_manager"},
		{&gtk.webViewLoadHTML, wk, "webkit_web_view_load_html"},
		{&gtk.webViewRunJS, wk, "webkit_web_view_run_javascript"},
		{&gtk.webViewBackground, wk, "webkit_web_view_set_background_color"},
		{&gtk.resultValue, wk, "webkit_javascript_result_get_js_value"},
		{&gtk.jsValueString, jsc, "jsc_value_to_string"},
	} {
		purego.RegisterLibFunc(f.fn, f.lib, f.sym)
	}
	return gtk.initCheck(nil, nil) // false without a display
}

// Work for the GTK thread, run by one idle callback.
var gtkQueue struct {
	sync.Mutex
	fns []func()
}

var idleCallback = purego.NewCallback(func(uintptr) uintptr {
	gtkQueue.Lock()
	fns := gtkQueue.fns
	gtkQueue.fns = nil
	gtkQueue.Unlock()
	for _, f := range fns {
		f()
	}
	return 0 // G_SOURCE_REMOVE
})

func onGtk(f func()) {
	gtkQueue.Lock()
	gtkQueue.fns = append(gtkQueue.fns, f)
	gtkQueue.Unlock()
	gtk.idleAdd(idleCallback, 0)
}

// The one launcher window on screen, if any; only the GTK thread touches it.
var linuxLauncher struct {
	window, view uintptr
	h            launcherHandlers
	closed       chan struct{}
}

var destroyCallback = purego.NewCallback(func(widget, data uintptr) uintptr {
	if linuxLauncher.closed != nil {
		close(linuxLauncher.closed)
		linuxLauncher.closed = nil
	}
	linuxLauncher.window, linuxLauncher.view = 0, 0
	return 0
})

var messageCallback = purego.NewCallback(func(manager, result, data uintptr) uintptr {
	var msg struct{ Fn, Arg string }
	if json.Unmarshal([]byte(gtk.jsValueString(gtk.resultValue(result))), &msg) != nil {
		return 0
	}
	h := linuxLauncher.h
	switch msg.Fn {
	case "watch":
		reply, _ := json.Marshal(h.watch(msg.Arg))
		gtk.webViewRunJS(linuxLauncher.view, "window._cpResolve("+string(reply)+")", 0, 0, 0)
	case "download":
		h.download()
	case "licenses":
		h.licenses()
	case "forget":
		h.forget(msg.Arg)
	case "setVpn":
		h.setVpn(msg.Arg)
	}
	return 0
})

func webViewOpener(string) windowOpener {
	if !gtk.ok {
		return nil
	}
	return func(html string, h launcherHandlers) launcherWindow {
		closed := make(chan struct{})
		onGtk(func() {
			m := gtk.managerNew()
			gtk.managerAddScript(m, gtk.userScriptNew(linuxBridge, 1, 0, 0, 0)) // top frame, document start
			gtk.signalConnect(m, "script-message-received::cp", messageCallback, 0, 0, 0)
			gtk.managerRegister(m, "cp")
			v := gtk.webViewNew(m)
			bg := [4]float64{0x0f / 255.0, 0x11 / 255.0, 0x15 / 255.0, 1} // no white flash before the page
			gtk.webViewBackground(v, &bg)
			w := gtk.windowNew(0)
			gtk.windowSetTitle(w, "CanalPeer")
			gtk.windowSetIconName(w, "canalpeer")
			gtk.windowSetSize(w, 480, 680)
			gtk.windowSetPosition(w, 1) // centre
			gtk.containerAdd(w, v)
			linuxLauncher.window, linuxLauncher.view, linuxLauncher.h, linuxLauncher.closed = w, v, h, closed
			gtk.signalConnect(w, "destroy", destroyCallback, 0, 0, 0)
			gtk.webViewLoadHTML(v, html, 0)
			gtk.widgetShowAll(w)
			gtk.windowPresent(w)
		})
		return launcherWindow{
			close: func() {
				onGtk(func() {
					if linuxLauncher.window != 0 {
						gtk.widgetDestroy(linuxLauncher.window)
					}
				})
			},
			closed: closed,
		}
	}
}

func urlEvents() <-chan string { return nil }

// runMain gives the main thread to GTK when there is a launcher to show.
func runMain(app func()) {
	if gtk.ok = loadGtk(); !gtk.ok {
		app()
		return
	}
	go app()
	gtk.main()
}
