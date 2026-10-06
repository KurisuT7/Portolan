package recovery

import (
	"errors"
	"fmt"
	"io/fs"
)

// validateWriterIdentity refuses root: the panel runs as an unprivileged
// account and must be able to open the database the importer creates.
func validateWriterIdentity(euid int) error {
	if euid == 0 {
		return errors.New("refusing to create a root-owned recovery database; run the importer as the account that runs the panel, for example with sudo -u portolan-panel")
	}
	return nil
}

func validateDatabaseIdentity(ownerUID, ownerGID, expectedUID, expectedGID int, mode fs.FileMode) error {
	if !mode.IsRegular() {
		return errors.New("candidate database is not a regular file")
	}
	if ownerUID != expectedUID || ownerGID != expectedGID {
		return fmt.Errorf("owner is uid:gid %d:%d, expected current process %d:%d", ownerUID, ownerGID, expectedUID, expectedGID)
	}
	if mode.Perm() != 0o600 {
		return fmt.Errorf("mode is %04o, expected 0600", mode.Perm())
	}
	return nil
}
