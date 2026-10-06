package recovery

import (
	"io/fs"
	"strings"
	"testing"
)

func TestValidateWriterIdentityRejectsRoot(t *testing.T) {
	if err := validateWriterIdentity(0); err == nil || !strings.Contains(err.Error(), "root-owned") {
		t.Fatalf("expected root writer rejection, got %v", err)
	}
	if err := validateWriterIdentity(1001); err != nil {
		t.Fatalf("unprivileged writer rejected: %v", err)
	}
}

func TestValidateDatabaseIdentityRequiresCurrentOwnerAnd0600(t *testing.T) {
	tests := []struct {
		name string
		uid  int
		gid  int
		mode fs.FileMode
		want string
	}{
		{name: "wrong uid", uid: 0, gid: 1001, mode: 0o600, want: "owner is"},
		{name: "wrong gid", uid: 1001, gid: 0, mode: 0o600, want: "owner is"},
		{name: "group readable", uid: 1001, gid: 1001, mode: 0o640, want: "expected 0600"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateDatabaseIdentity(test.uid, test.gid, 1001, 1001, test.mode); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q failure, got %v", test.want, err)
			}
		})
	}
	if err := validateDatabaseIdentity(1001, 1001, 1001, 1001, 0o600); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}
}
