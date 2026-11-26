//go:build darwin

package main

import (
	"os/exec"
	"strings"
)

// alert tells the user something went wrong before any window exists.
func alert(text string) {
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(text)
	exec.Command("osascript", "-e", `display alert "CanalPeer" message "`+esc+`" as critical`).Run()
}
