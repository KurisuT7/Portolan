package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/KurisuT7/Portolan/internal/model"
)

var versionToken = regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]{0,63}$`)

// Inspect reports the revision of the active release, the installed core
// versions and the systemd state of every service the active release defines.
func Inspect(ctx context.Context, options Options) (model.RuntimeStatus, error) {
	if err := defaultsAndValidate(&options); err != nil {
		return model.RuntimeStatus{}, err
	}
	status := model.RuntimeStatus{
		SingBoxVersion: binaryVersion(ctx, options, options.SingBoxBinary, 2, "version"),
		RealmVersion:   binaryVersion(ctx, options, options.RealmBinary, 1, "-v"),
		Units:          []model.UnitStatus{},
	}
	release, err := filepath.EvalSymlinks(filepath.Join(options.RuntimeRoot, "current"))
	if errors.Is(err, os.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return model.RuntimeStatus{}, err
	}
	data, err := os.ReadFile(filepath.Join(release, "manifest.json"))
	if err != nil {
		return model.RuntimeStatus{}, err
	}
	var manifest struct {
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return model.RuntimeStatus{}, fmt.Errorf("read release manifest: %w", err)
	}
	status.AppliedRevision = manifest.Revision
	configs, err := serviceConfigs(options, release)
	if err != nil {
		return model.RuntimeStatus{}, err
	}
	units := slices.Sorted(maps.Keys(configs))
	if len(units) == 0 {
		return status, nil
	}
	output, err := options.RunCommand(ctx, options.SystemctlBinary, append([]string{"show", "--property=Id,ActiveState,SubState"}, units...)...)
	if err != nil {
		return model.RuntimeStatus{}, fmt.Errorf("systemctl show: %w: %s", err, output)
	}
	reported := map[string]model.UnitStatus{}
	for _, block := range strings.Split(output, "\n\n") {
		var unit model.UnitStatus
		for _, line := range strings.Split(block, "\n") {
			key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
			switch key {
			case "Id":
				unit.Name = value
			case "ActiveState":
				unit.ActiveState = value
			case "SubState":
				unit.SubState = value
			}
		}
		reported[unit.Name] = unit
	}
	for _, name := range units {
		unit, ok := reported[name]
		if !ok {
			return model.RuntimeStatus{}, fmt.Errorf("systemctl did not report %s", name)
		}
		status.Units = append(status.Units, unit)
	}
	return status, nil
}

// binaryVersion returns the version token from the first output line, or an
// empty string when the binary cannot report one.
func binaryVersion(ctx context.Context, options Options, binary string, field int, arguments ...string) string {
	output, err := options.RunCommand(ctx, binary, arguments...)
	if err != nil {
		return ""
	}
	fields := strings.Fields(strings.SplitN(output, "\n", 2)[0])
	if len(fields) <= field || !versionToken.MatchString(fields[field]) {
		return ""
	}
	return fields[field]
}
