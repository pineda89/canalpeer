//go:build darwin

package main

// macOS registers acestream:// from the app's Info.plist.
func ensureRegistered(exe, dataDir string) {}
