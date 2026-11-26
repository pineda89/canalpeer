package main

import (
	"log"
	"path/filepath"
	"runtime"

	"github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

var getDpiForSystem = windows.NewLazySystemDLL("user32.dll").NewProc("GetDpiForSystem")

// webViewOpener opens the launcher in WebView2, or is nil when this Windows
// has no WebView2 runtime (then the app works as before, player window only).
func webViewOpener(dataDir string) windowOpener {
	if _, err := webviewloader.GetInstalledVersion(); err != nil {
		log.Print("no WebView2: ", err)
		return nil
	}
	return func(html string, h launcherHandlers) launcherWindow {
		closed := make(chan struct{})
		ready := make(chan webview2.WebView)
		go func() {
			// A Win32 window belongs to the thread that made it, and so does its
			// message loop; WebView2 also wants that thread in a COM apartment.
			runtime.LockOSThread()
			defer close(closed)
			windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)
			defer windows.CoUninitialize()
			// The window size is in physical pixels: scale it for 150 %, 200 % screens.
			scale := uint(96)
			if dpi, _, _ := getDpiForSystem.Call(); dpi != 0 {
				scale = uint(dpi)
			}
			w := webview2.NewWithOptions(webview2.WebViewOptions{
				DataPath:  filepath.Join(dataDir, "webview2"),
				AutoFocus: true,
				WindowOptions: webview2.WindowOptions{
					Title: "CanalPeer", Width: 480 * scale / 96, Height: 680 * scale / 96, IconId: 1, Center: true,
				},
			})
			ready <- w
			if w == nil {
				return
			}
			w.Bind("cpWatch", h.watch)
			w.Bind("cpDownload", h.download)
			w.Bind("cpLicenses", h.licenses)
			w.Bind("cpForget", h.forget)
			w.Bind("cpSetVpn", h.setVpn)
			w.SetHtml(html)
			w.Run()
		}()
		w := <-ready
		if w == nil {
			log.Print("the launcher window could not be made")
			return launcherWindow{close: func() {}, closed: closed, failed: true}
		}
		return launcherWindow{
			close:  func() { w.Dispatch(w.Destroy) },
			closed: closed,
		}
	}
}
