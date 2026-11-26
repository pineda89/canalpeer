//go:build !windows

package main

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestHelperParent is not a test: TestSignalStopsChild runs it as a separate
// process that owns a child, the way the app owns mpv.
func TestHelperParent(t *testing.T) {
	if os.Getenv("CANALPEER_HELPER") != "1" {
		t.Skip("helper process only")
	}
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	killOnExit(func() { cmd.Process.Kill() })
	os.Stdout.WriteString(strconv.Itoa(cmd.Process.Pid) + "\n")
	time.Sleep(time.Minute)
}

func TestSignalStopsChild(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperParent$")
	cmd.Env = append(os.Environ(), "CANALPEER_HELPER=1")
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	child, _ := strconv.Atoi(strings.TrimSpace(line))
	cmd.Process.Signal(syscall.SIGTERM)
	cmd.Wait()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if errors.Is(syscall.Kill(child, 0), syscall.ESRCH) {
			return
		}
	}
	syscall.Kill(child, syscall.SIGKILL)
	t.Fatal("the child outlived its parent")
}
