package recovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/KurisuT7/Portolan/internal/store"
	"github.com/KurisuT7/Portolan/internal/vault"
	_ "modernc.org/sqlite"
)

func WriteDatabase(ctx context.Context, outputPath, masterKey string, recovered Result) (err error) {
	if !filepath.IsAbs(outputPath) {
		return errors.New("output database path must be absolute")
	}
	if len(recovered.Servers) == 0 {
		return errors.New("refusing to create an empty recovery database")
	}
	outputPath = filepath.Clean(outputPath)
	parent := filepath.Dir(outputPath)
	if info, statErr := os.Stat(parent); statErr != nil || !info.IsDir() {
		return fmt.Errorf("output parent must already exist: %s", parent)
	}
	temporary := outputPath + ".importing"
	for _, path := range []string{outputPath, outputPath + "-wal", outputPath + "-shm", temporary, temporary + "-wal", temporary + "-shm"} {
		if _, statErr := os.Lstat(path); statErr == nil {
			return fmt.Errorf("refusing to overwrite existing database artifact: %s", path)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}
	temporaryOwned := false
	cleanup := func() {
		if !temporaryOwned {
			return
		}
		for _, path := range []string{temporary, temporary + "-wal", temporary + "-shm"} {
			_ = os.Remove(path)
		}
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	reserved, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("reserve recovery database: %w", err)
	}
	temporaryOwned = true
	if err := reserved.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close recovery database reservation: %w", err)
	}

	secretVault, err := vault.New(masterKey)
	if err != nil {
		return fmt.Errorf("initialize recovery vault: %w", err)
	}
	database, err := store.Open(temporary, secretVault)
	if err != nil {
		return fmt.Errorf("create recovery database: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = database.Close()
		}
	}()

	for _, server := range recovered.Servers {
		if _, _, createErr := database.CreateServer(ctx, server); createErr != nil {
			return fmt.Errorf("create recovered server %s: %w", server.ID, createErr)
		}
	}
	for _, node := range recovered.Nodes {
		if _, createErr := database.CreateNode(ctx, node.Node, node.Profile); createErr != nil {
			return fmt.Errorf("create recovered node %s: %w", node.Node.ID, createErr)
		}
	}
	for _, forward := range recovered.Forwards {
		if _, createErr := database.CreateForward(ctx, forward); createErr != nil {
			return fmt.Errorf("create recovered forward %s: %w", forward.ID, createErr)
		}
	}
	if err := database.Close(); err != nil {
		return fmt.Errorf("close recovery database: %w", err)
	}
	closed = true
	if err := checkpointAndVerify(temporary); err != nil {
		return err
	}
	for _, sidecar := range []string{temporary + "-wal", temporary + "-shm"} {
		if _, statErr := os.Lstat(sidecar); statErr == nil {
			return fmt.Errorf("SQLite sidecar remained after checkpoint: %s", sidecar)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		return err
	}
	if err := verifyDatabaseIdentity(temporary); err != nil {
		return fmt.Errorf("verify recovery database ownership and mode: %w", err)
	}
	if err := publishNoReplace(temporary, outputPath); err != nil {
		return fmt.Errorf("publish recovery database without overwrite: %w", err)
	}
	temporaryOwned = false
	return nil
}

func checkpointAndVerify(path string) error {
	database, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	if _, err := database.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		_ = database.Close()
		return fmt.Errorf("checkpoint recovery database: %w", err)
	}
	var result string
	if err := database.QueryRow("PRAGMA quick_check").Scan(&result); err != nil {
		_ = database.Close()
		return fmt.Errorf("quick_check recovery database: %w", err)
	}
	if result != "ok" {
		_ = database.Close()
		return fmt.Errorf("recovery database quick_check=%s", result)
	}
	if err := database.Close(); err != nil {
		return err
	}
	return nil
}
