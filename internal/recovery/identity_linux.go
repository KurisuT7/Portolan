//go:build linux

package recovery

import (
	"fmt"
	"os"
	"syscall"
)

func ValidateWriter() error {
	return validateWriterIdentity(os.Geteuid())
}

func verifyDatabaseIdentity(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot read Unix ownership for %s", path)
	}
	return validateDatabaseIdentity(int(stat.Uid), int(stat.Gid), os.Geteuid(), os.Getegid(), info.Mode())
}

func databaseModeEnforced() bool {
	return true
}
