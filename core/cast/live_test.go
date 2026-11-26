//go:build live

package cast

import (
	"context"
	"testing"
	"time"
)

func TestLiveSearch(t *testing.T) {
	rs, err := Search(context.Background(), 4*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		t.Logf("%+v", r)
	}
	if len(rs) == 0 {
		t.Fatal("no renderers found")
	}
}
