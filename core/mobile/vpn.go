package mobile

import "canalpeer.com/core/route"

// SetVpn sets the WARP mode for the whole process ("auto" or "", "on", "off")
// and the directory holding WARP's account and the switch memory (DataDir).
func SetVpn(mode, dataDir string) { route.Configure(mode, dataDir) }

// VpnInUse reports whether connections go through WARP right now.
func VpnInUse() bool { return route.InUse() }

// ProxyAddr is "127.0.0.1:<port>" of a local SOCKS5 proxy that follows the
// route, for traffic outside Go (the update check); "" while WARP is not
// wanted, and then such traffic goes direct.
func ProxyAddr() string { return route.ProxyAddr() }
