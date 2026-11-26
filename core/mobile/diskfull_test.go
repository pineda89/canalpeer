package mobile

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestDiskFull(t *testing.T) {
	if !diskFull(fmt.Errorf("write 3.part: %w", syscall.ENOSPC)) {
		t.Fatal("ENOSPC is a full disk")
	}
	if diskFull(errors.New("permission denied")) {
		t.Fatal("other errors are not")
	}
}
