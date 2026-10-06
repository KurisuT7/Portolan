//go:build !linux

package recovery

import (
	"errors"
	"os"
)

func ValidateWriter() error {
	return nil
}

func verifyDatabaseIdentity(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("candidate database is not a regular file")
	}
	return nil
}

func databaseModeEnforced() bool {
	return false
}
