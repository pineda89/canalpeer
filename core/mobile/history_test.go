package mobile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

var t0 = time.Unix(1_800_000_000, 0)

func historyIDs(dir string) []string {
	var ids []string
	for _, e := range loadHistory(dir) {
		ids = append(ids, e.ID)
	}
	return ids
}

func mustRecord(t *testing.T, dir, id, name string, at time.Time) {
	t.Helper()
	if err := recordHistory(dir, id, name, at); err != nil {
		t.Fatal(err)
	}
}

func TestRecordNewestFirst(t *testing.T) {
	dir := t.TempDir()
	mustRecord(t, dir, "a", "A", t0)
	mustRecord(t, dir, "b", "B", t0.Add(time.Second))
	mustRecord(t, dir, "c", "C", t0.Add(2*time.Second))
	h := loadHistory(dir)
	if got := historyIDs(dir); !slices.Equal(got, []string{"c", "b", "a"}) {
		t.Fatalf("ids %v", got)
	}
	if h[0].At != t0.Unix()+2 || h[2].At != t0.Unix() || h[1].Name != "B" {
		t.Fatalf("entries %+v", h)
	}
}

func TestRecordMovesToTop(t *testing.T) {
	dir := t.TempDir()
	mustRecord(t, dir, "a", "old", t0)
	mustRecord(t, dir, "b", "B", t0.Add(time.Second))
	mustRecord(t, dir, "a", "new", t0.Add(2*time.Second))
	h := loadHistory(dir)
	if len(h) != 2 || h[0].ID != "a" || h[1].ID != "b" {
		t.Fatalf("entries %+v", h)
	}
	if h[0].Name != "new" || h[0].At != t0.Unix()+2 {
		t.Fatalf("top entry not refreshed: %+v", h[0])
	}
}

func TestRecordCapsAt20(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i <= 25; i++ {
		mustRecord(t, dir, fmt.Sprint(i), "", t0.Add(time.Duration(i)*time.Second))
	}
	ids := historyIDs(dir)
	if len(ids) != 20 || ids[0] != "25" || ids[19] != "6" {
		t.Fatalf("ids %v", ids)
	}
}

func TestForget(t *testing.T) {
	dir := t.TempDir()
	mustRecord(t, dir, "a", "", t0)
	mustRecord(t, dir, "b", "", t0.Add(time.Second))
	if err := forgetHistory(dir, "a"); err != nil {
		t.Fatal(err)
	}
	if got := historyIDs(dir); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("ids %v", got)
	}
	if err := forgetHistory(dir, "nope"); err != nil {
		t.Fatalf("forgetting an unknown id: %v", err)
	}
}

func TestCorruptFileReadsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "history.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if h := loadHistory(dir); h == nil || len(h) != 0 {
		t.Fatalf("corrupt file read as %#v", h)
	}
	mustRecord(t, dir, "x", "", t0)
	if got := historyIDs(dir); !slices.Equal(got, []string{"x"}) {
		t.Fatalf("ids %v", got)
	}
}

func TestMissingFileReadsEmpty(t *testing.T) {
	if h := loadHistory(t.TempDir()); h == nil || len(h) != 0 {
		t.Fatalf("missing file read as %#v", h)
	}
}

func TestRecordCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	mustRecord(t, dir, "x", "", t0)
	if got := historyIDs(dir); !slices.Equal(got, []string{"x"}) {
		t.Fatalf("ids %v", got)
	}
}

func TestRecordLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"a", "b", "c"} {
		mustRecord(t, dir, id, "", t0)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "history.json" {
		t.Fatalf("dir holds %v", entries)
	}
}

func TestRecordConcurrent(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := recordHistory(dir, fmt.Sprint(i), "", t0); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := len(loadHistory(dir)); n != 20 {
		t.Fatalf("%d entries survived of 20", n)
	}
}

func TestHistoryJSON(t *testing.T) {
	dir := t.TempDir()
	mustRecord(t, dir, "a", "A", t0)
	mustRecord(t, dir, "b", "", t0.Add(time.Second))
	var got []map[string]any
	if err := json.Unmarshal([]byte(History(dir)), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0]["id"] != "b" || got[0]["name"] != "" || got[1]["name"] != "A" || got[1]["at"] != float64(t0.Unix()) {
		t.Fatalf("history %v", got)
	}
	Forget(dir, "b")
	if ids := historyIDs(dir); !slices.Equal(ids, []string{"a"}) {
		t.Fatalf("after Forget %v", ids)
	}
}
