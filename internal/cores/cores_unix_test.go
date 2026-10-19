//go:build unix

package cores_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/KurisuT7/Portolan/internal/cores"
	"github.com/KurisuT7/Portolan/internal/cores/corestest"
	"github.com/KurisuT7/Portolan/internal/model"
)

// The Agent runs with umask 0027 while the cores run as another user.
func TestExtractBinaryIsExecutableByOtherUsers(t *testing.T) {
	previous := syscall.Umask(0o027)
	defer syscall.Umask(previous)
	directory := t.TempDir()
	archive := filepath.Join(directory, "sing-box.tar.gz")
	if err := os.WriteFile(archive, corestest.Archive(model.CoreSingBox, "1.14.3", []byte("new")), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(directory, "sing-box")
	if err := cores.ExtractBinary(archive, model.CoreSingBox, binary); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o755 {
		t.Fatalf("mode = %o, want 755", mode)
	}
}
