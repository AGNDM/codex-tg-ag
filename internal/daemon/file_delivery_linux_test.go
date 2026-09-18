//go:build linux

package daemon

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestOpenProjectDeliveryFileRejectsFIFOWithoutBlocking(t *testing.T) {
	project := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(project, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openProjectDeliveryFile(project, "pipe", 1024); err == nil {
		t.Fatal("openProjectDeliveryFile accepted a FIFO")
	}
}
