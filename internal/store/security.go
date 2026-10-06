package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"
)

const totpSealContext = "admin-totp"

// AdminTOTP returns the administrator's two-step verification secret and the
// last accepted time step. enabled is false when two-step verification is off.
func (s *Store) AdminTOTP(ctx context.Context) (secret string, lastStep int64, enabled bool, err error) {
	var sealed string
	err = s.db.QueryRowContext(ctx, `SELECT sealed_secret,last_step FROM admin_totp WHERE id=1`).Scan(&sealed, &lastStep)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	plaintext, err := s.vault.Open(sealed, totpSealContext)
	if err != nil {
		return "", 0, false, err
	}
	return string(plaintext), lastStep, true, nil
}

// EnableAdminTOTP stores a verified secret. step is the step of the code that
// confirmed it, so that code cannot be used again to log in.
func (s *Store) EnableAdminTOTP(ctx context.Context, secret string, step int64) error {
	sealed, err := s.vault.Seal([]byte(secret), totpSealContext)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO admin_totp(id,sealed_secret,last_step,enabled_at) VALUES(1,?,?,?)
		ON CONFLICT(id) DO UPDATE SET sealed_secret=excluded.sealed_secret,last_step=excluded.last_step,enabled_at=excluded.enabled_at`,
		sealed, step, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// ConsumeAdminTOTPStep records step as used. It reports false when the same
// or a later step was already accepted, which rejects a replayed code even
// when two requests race.
func (s *Store) ConsumeAdminTOTPStep(ctx context.Context, step int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE admin_totp SET last_step=? WHERE id=1 AND last_step<?`, step, step)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

// DisableAdminTOTP turns two-step verification off.
func (s *Store) DisableAdminTOTP(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM admin_totp`)
	return err
}

// DisableAdminTOTPAt turns two-step verification off in the database at path
// without the master key. It is the recovery path for a lost authenticator and
// reports whether two-step verification was enabled.
func DisableAdminTOTPAt(ctx context.Context, path string) (bool, error) {
	// Opening a missing path would create an empty database.
	if info, err := os.Stat(path); err != nil {
		return false, err
	} else if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a database file", path)
	}
	database, err := Open(path, nil)
	if err != nil {
		return false, err
	}
	defer database.Close()
	result, err := database.db.ExecContext(ctx, `DELETE FROM admin_totp`)
	if err != nil {
		return false, err
	}
	removed, err := result.RowsAffected()
	return removed > 0, err
}
