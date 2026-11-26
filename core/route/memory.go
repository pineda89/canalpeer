package route

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// memoryFile in dataDir holds when the last automatic switch to WARP expires.
const memoryFile = "vpn-switch.json"

// switchTTL is how long an automatic switch is remembered.
const switchTTL = 3 * time.Hour

type memory struct {
	Until time.Time `json:"until"`
}

func loadMemory(dataDir string) time.Time {
	if dataDir == "" {
		return time.Time{}
	}
	b, err := os.ReadFile(filepath.Join(dataDir, memoryFile))
	if err != nil {
		return time.Time{}
	}
	var m memory
	if json.Unmarshal(b, &m) != nil {
		return time.Time{}
	}
	return m.Until
}

// Switched records that Automatic switched to WARP at now: it stays in use
// until now+3h, across restarts.
func Switched(now time.Time) {
	mu.Lock()
	defer mu.Unlock()
	st.until = now.Add(switchTTL)
	if st.dataDir != "" {
		b, _ := json.Marshal(memory{Until: st.until})
		_ = os.WriteFile(filepath.Join(st.dataDir, memoryFile), b, 0o600) // ponytail: best effort, a lost write only forgets the switch
	}
}

// Remembered reports whether a switch is remembered at now.
func Remembered(now time.Time) bool {
	mu.Lock()
	defer mu.Unlock()
	return now.Before(st.until)
}

// ForgetSwitch drops the remembered switch.
func ForgetSwitch() {
	mu.Lock()
	defer mu.Unlock()
	st.forget()
}

func (s *state) forget() {
	s.until = time.Time{}
	if s.dataDir != "" {
		os.Remove(filepath.Join(s.dataDir, memoryFile))
	}
}
