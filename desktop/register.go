package main

import "strings"

type regAction int

const (
	regNothing regAction = iota
	regRegister
	regAsk
)

// registrationDecision decides what to do with acestream:// given the command
// currently registered for it. Another app (Ace Stream) is only replaced after
// the user says yes, once; a "no" is never asked again.
func registrationDecision(current, ourExe string, saved *bool) regAction {
	exe := commandExe(current)
	switch {
	case exe == "":
		return regRegister
	case strings.EqualFold(exe, ourExe):
		return regNothing
	case strings.EqualFold(exe[strings.LastIndexAny(exe, `\/`)+1:], "CanalPeer.exe"):
		return regRegister // our own app from an older folder: follow the move
	case saved != nil && *saved:
		return regRegister
	case saved != nil:
		return regNothing
	}
	return regAsk
}

// commandExe is the program of a shell command line such as `"C:\x\a.exe" "%1"`.
func commandExe(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if strings.HasPrefix(cmd, `"`) {
		if end := strings.Index(cmd[1:], `"`); end >= 0 {
			return cmd[1 : end+1]
		}
	}
	if f := strings.Fields(cmd); len(f) > 0 {
		return f[0]
	}
	return ""
}
