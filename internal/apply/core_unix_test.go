//go:build unix

package apply

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepairCoreModesRestoresServiceAccess(t *testing.T) {
	directory := t.TempDir()
	singBox := filepath.Join(directory, "sing-box")
	if err := os.WriteFile(singBox, []byte("core"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(singBox, 0o750); err != nil {
		t.Fatal(err)
	}
	options := Options{RuntimeRoot: filepath.Join(directory, "runtime"), SingBoxBinary: singBox, RealmBinary: filepath.Join(directory, "realm")}
	if err := RepairCoreModes(options); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(singBox)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o755 {
		t.Fatalf("mode = %o, want 755", mode)
	}
}
