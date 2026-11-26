package main

import (
	"log"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

// ensureRegistered makes this exe open acestream:// links for the current user
// (HKCU: no admin, no installer), asking first if another app has them.
func ensureRegistered(exe, dataDir string) {
	current := ""
	if k, err := registry.OpenKey(registry.CLASSES_ROOT, `acestream\shell\open\command`, registry.QUERY_VALUE); err == nil {
		current, _, _ = k.GetStringValue("")
		k.Close()
	}
	s := loadSettings(dataDir)
	switch registrationDecision(current, exe, s.HandleLinks) {
	case regNothing:
		return
	case regAsk:
		yes := askYesNo("CanalPeer", "Open acestream:// links with CanalPeer?")
		s.HandleLinks = &yes
		saveSettings(dataDir, s)
		if !yes {
			return
		}
	}
	if err := register(exe); err != nil {
		log.Print("register acestream://: ", err)
	}
}

func register(exe string) error {
	set := func(path, name, value string) error {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+path, registry.SET_VALUE)
		if err != nil {
			return err
		}
		defer k.Close()
		return k.SetStringValue(name, value)
	}
	for _, v := range [][3]string{
		{`acestream`, ``, `URL:AceStream link`},
		{`acestream`, `URL Protocol`, ``},
		{`acestream\DefaultIcon`, ``, `"` + exe + `",0`},
		{`acestream\shell\open\command`, ``, `"` + exe + `" "%1"`},
	} {
		if err := set(v[0], v[1], v[2]); err != nil {
			return err
		}
	}
	return nil
}

func askYesNo(title, text string) bool {
	const mbYesNo, mbIconQuestion, idYes = 0x4, 0x20, 6
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(text)
	r, _, _ := syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW").Call(
		0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbYesNo|mbIconQuestion)
	return r == idYes
}

// alert tells the user something went wrong before any window exists.
func alert(text string) {
	const mbOK, mbIconError = 0x0, 0x10
	t, _ := syscall.UTF16PtrFromString("CanalPeer")
	m, _ := syscall.UTF16PtrFromString(text)
	syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW").Call(
		0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbOK|mbIconError)
}
