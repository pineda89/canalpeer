package main

import "testing"

func TestRegistrationDecision(t *testing.T) {
	yes, no := true, false
	const ours = `C:\cp\CanalPeer.exe`
	cases := []struct {
		cur   string
		saved *bool
		want  regAction
	}{
		{"", nil, regRegister},                                    // nobody handles it
		{`"C:\cp\CanalPeer.exe" "%1"`, nil, regNothing},           // already ours
		{`"c:\CP\canalpeer.exe" "%1"`, nil, regNothing},           // Windows paths ignore case
		{`"C:\AceStream\ace_player.exe" "%1"`, nil, regAsk},       // someone else: ask
		{`"C:\AceStream\ace_player.exe" "%1"`, &yes, regRegister}, // said yes, Ace Stream took it back
		{`"C:\AceStream\ace_player.exe" "%1"`, &no, regNothing},   // said no: never ask again
		{`"D:\old\CanalPeer.exe" "%1"`, nil, regRegister},         // our app, moved: follow it
	}
	for _, c := range cases {
		if got := registrationDecision(c.cur, ours, c.saved); got != c.want {
			t.Errorf("%q saved=%v: got %v want %v", c.cur, c.saved, got, c.want)
		}
	}
}
