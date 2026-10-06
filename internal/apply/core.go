package apply

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/KurisuT7/portolan/internal/cores"
	"github.com/KurisuT7/portolan/internal/model"
)

var (
	ErrCoreArchive        = errors.New("core archive is unusable")
	ErrCoreRejected       = errors.New("new sing-box rejected the active configuration")
	ErrCoreRestart        = errors.New("services did not run with the new core")
	ErrCoreRollbackFailed = errors.New("restoring the previous core failed")
)

// UpdateCore replaces a core binary with the one in a verified release
// archive. The new binary must run on this host, and a new sing-box must
// accept the active release first. Every service
// of the active release that runs the core is restarted and must keep running
// with its sockets; otherwise the previous binary is restored and restarted.
func UpdateCore(ctx context.Context, core model.Core, archive string, options Options) error {
	if err := defaultsAndValidate(&options); err != nil {
		return err
	}
	binary := options.SingBoxBinary
	if core == model.CoreRealm {
		binary = options.RealmBinary
	}
	staged := filepath.Join(filepath.Dir(binary), "."+filepath.Base(binary)+".new")
	defer os.Remove(staged)
	if err := cores.ExtractBinary(archive, core, staged); err != nil {
		return errors.Join(ErrCoreArchive, err)
	}
	versionArg := "version"
	if core == model.CoreRealm {
		versionArg = "-v"
	}
	if output, err := options.RunCommand(ctx, staged, versionArg); err != nil {
		return fmt.Errorf("%w: it does not run on this host: %w: %s", ErrCoreArchive, err, output)
	}
	release, err := filepath.EvalSymlinks(filepath.Join(options.RuntimeRoot, "current"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if release != "" && core == model.CoreSingBox {
		if output, err := options.RunCommand(ctx, staged, "check",
			"-c", filepath.Join(release, "sing-box", "00-base.json"),
			"-C", filepath.Join(release, "sing-box", "conf.d")); err != nil {
			return fmt.Errorf("%w: %w: %s", ErrCoreRejected, err, output)
		}
	}
	units, listeners, err := coreUnits(options, release, core)
	if err != nil {
		return err
	}
	previous := binary + ".previous"
	if err := os.Remove(previous); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Link(binary, previous); err != nil {
		return err
	}
	if err := os.Rename(staged, binary); err != nil {
		return err
	}
	if options.SkipServiceActions || len(units) == 0 {
		_ = os.Remove(previous)
		return nil
	}
	if err := restartUnits(ctx, options, units, listeners); err != nil {
		// A cancelled update still needs a bounded opportunity to restore service.
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		failure := fmt.Errorf("%w: %w", ErrCoreRestart, err)
		if restoreErr := os.Rename(previous, binary); restoreErr != nil {
			return errors.Join(failure, ErrCoreRollbackFailed, restoreErr)
		}
		if rollbackErr := restartUnits(rollbackCtx, options, units, listeners); rollbackErr != nil {
			return errors.Join(failure, ErrCoreRollbackFailed, rollbackErr)
		}
		return failure
	}
	_ = os.Remove(previous)
	return nil
}

// coreUnits lists the services of a release that run the core.
func coreUnits(options Options, release string, core model.Core) ([]string, map[string][]listener, error) {
	if release == "" {
		return nil, nil, nil
	}
	configs, err := serviceConfigs(options, release)
	if err != nil {
		return nil, nil, err
	}
	var units []string
	for unit := range configs {
		if (core == model.CoreSingBox && unit == options.SingBoxService) ||
			(core == model.CoreRealm && strings.HasPrefix(unit, options.RealmServicePrefix)) {
			units = append(units, unit)
		}
	}
	slices.Sort(units)
	listeners, err := releaseListeners(release)
	return units, listeners, err
}
