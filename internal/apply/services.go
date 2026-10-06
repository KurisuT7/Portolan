package apply

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/KurisuT7/portolan/internal/agentproto"
	"github.com/KurisuT7/portolan/internal/model"
)

// settleDelay covers services that exit or restart shortly after systemd
// reports them started: Type=simple units count as active once forked.
var settleDelay = 2 * time.Second

// serviceChange describes a configuration change, not every service in a
// desired-state snapshot. Unchanged services keep their existing connections.
type serviceChange struct {
	unit   string
	before bool
	after  bool
}

// listener is a socket a unit must own once it is running.
type listener struct {
	Network string `json:"network"`
	Port    uint16 `json:"port"`
}

type startedUnit struct {
	unit string
	pid  int
}

type unitStatus struct {
	active string
	sub    string
	pid    int
}

func planServices(options Options, before, after string) ([]serviceChange, error) {
	old, err := serviceConfigs(options, before)
	if err != nil {
		return nil, err
	}
	next, err := serviceConfigs(options, after)
	if err != nil {
		return nil, err
	}
	units := make([]string, 0, len(old)+len(next))
	for unit := range old {
		units = append(units, unit)
	}
	for unit := range next {
		units = append(units, unit)
	}
	slices.Sort(units)
	var changes []serviceChange
	for _, unit := range slices.Compact(units) {
		previous, existed := old[unit]
		desired, exists := next[unit]
		if existed == exists && bytes.Equal(previous, desired) {
			continue
		}
		changes = append(changes, serviceChange{unit: unit, before: existed, after: exists})
	}
	return changes, nil
}

func serviceConfigs(options Options, release string) (map[string][]byte, error) {
	configs := map[string][]byte{}
	if release == "" {
		return configs, nil
	}
	fragments, err := os.ReadDir(filepath.Join(release, "sing-box", "conf.d"))
	if err != nil {
		return nil, err
	}
	hasSingBox := slices.ContainsFunc(fragments, func(entry fs.DirEntry) bool { return !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" })
	if hasSingBox {
		var config bytes.Buffer
		err := filepath.WalkDir(filepath.Join(release, "sing-box"), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(release, path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&config, "%s\x00%d\x00", relative, len(data))
			config.Write(data)
			return nil
		})
		if err != nil {
			return nil, err
		}
		configs[options.SingBoxService] = config.Bytes()
	}
	entries, err := os.ReadDir(filepath.Join(release, "realm"))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".toml" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-len(".toml")]
		if !safeID.MatchString(id) {
			return nil, errors.New("unsafe Realm instance in release")
		}
		data, err := os.ReadFile(filepath.Join(release, "realm", entry.Name()))
		if err != nil {
			return nil, err
		}
		configs[options.RealmServicePrefix+id+".service"] = data
	}
	return configs, nil
}

// expectedListeners lists the TCP listeners and UDP sockets every unit of a
// snapshot must own. Protocol inbounds are checked on TCP, which all of them use.
func expectedListeners(options Options, payload agentproto.SyncPayload) map[string][]listener {
	listeners := map[string][]listener{}
	for _, node := range payload.Nodes {
		if node.Enabled {
			listeners[options.SingBoxService] = append(listeners[options.SingBoxService], listener{"tcp", node.ListenPort})
		}
	}
	for _, forward := range payload.Forwards {
		if !forward.Enabled {
			continue
		}
		unit := options.SingBoxService
		if forward.Engine == model.ForwardRealm {
			unit = options.RealmServicePrefix + forward.ID + ".service"
		}
		for _, network := range forward.Networks {
			listeners[unit] = append(listeners[unit], listener{network, forward.ListenPort})
		}
	}
	return listeners
}

func serviceCommand(ctx context.Context, options Options, arguments ...string) error {
	output, err := options.RunCommand(ctx, options.SystemctlBinary, arguments...)
	if err != nil {
		return fmt.Errorf("systemctl %v: %w: %s", arguments, err, output)
	}
	return nil
}

func activateServices(ctx context.Context, options Options, changes []serviceChange, listeners map[string][]listener) error {
	// Release all changed listeners before starting replacements, including
	// engine changes and port swaps between two instances.
	for _, change := range changes {
		if change.before {
			if err := serviceCommand(ctx, options, "stop", change.unit); err != nil {
				return err
			}
		}
	}
	var started []startedUnit
	for _, change := range changes {
		if !change.after {
			if err := serviceCommand(ctx, options, "disable", change.unit); err != nil {
				return err
			}
			continue
		}
		unit, err := startUnit(ctx, options, change.unit)
		if err != nil {
			return err
		}
		started = append(started, unit)
	}
	return verifyStarted(ctx, options, started, listeners)
}

func startUnit(ctx context.Context, options Options, unit string) (startedUnit, error) {
	if err := serviceCommand(ctx, options, "enable", "--now", unit); err != nil {
		return startedUnit{}, err
	}
	state, err := showUnit(ctx, options, unit)
	if err != nil {
		return startedUnit{}, err
	}
	return startedUnit{unit: unit, pid: state.pid}, nil
}

// verifyStarted waits for started units to settle, then requires each one to
// still run the main process it started with and to own its expected sockets.
func verifyStarted(ctx context.Context, options Options, units []startedUnit, listeners map[string][]listener) error {
	if len(units) == 0 {
		return nil
	}
	timer := time.NewTimer(settleDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	var failures []error
	for _, started := range units {
		failures = append(failures, verifyUnit(ctx, options, started, listeners[started.unit]))
	}
	return errors.Join(failures...)
}

func verifyUnit(ctx context.Context, options Options, started startedUnit, expected []listener) error {
	state, err := showUnit(ctx, options, started.unit)
	if err != nil {
		return err
	}
	if state.active != "active" || state.sub != "running" || state.pid == 0 || state.pid != started.pid {
		return fmt.Errorf("%s did not stay running: %s/%s", started.unit, state.active, state.sub)
	}
	if len(expected) == 0 {
		return nil
	}
	owned, err := socketInodes(options.ProcRoot, state.pid)
	if err != nil {
		return fmt.Errorf("%s: %w", started.unit, err)
	}
	for _, want := range expected {
		bound, err := ownsSocket(options.ProcRoot, want, owned)
		if err != nil {
			return err
		}
		if !bound {
			return fmt.Errorf("%s is not listening on %s port %d", started.unit, want.Network, want.Port)
		}
	}
	return nil
}

func showUnit(ctx context.Context, options Options, unit string) (unitStatus, error) {
	output, err := options.RunCommand(ctx, options.SystemctlBinary, "show", "--property=ActiveState,SubState,MainPID", unit)
	if err != nil {
		return unitStatus{}, fmt.Errorf("systemctl show %s: %w: %s", unit, err, output)
	}
	var state unitStatus
	for _, line := range strings.Split(output, "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "ActiveState":
			state.active = value
		case "SubState":
			state.sub = value
		case "MainPID":
			state.pid, _ = strconv.Atoi(value)
		}
	}
	return state, nil
}

// socketInodes lists the socket inodes held by a process.
func socketInodes(procRoot string, pid int) (map[string]bool, error) {
	directory := filepath.Join(procRoot, strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	inodes := map[string]bool{}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(directory, entry.Name()))
		if err != nil {
			continue // the descriptor was closed while listing
		}
		if inode, ok := strings.CutPrefix(target, "socket:["); ok {
			inodes[strings.TrimSuffix(inode, "]")] = true
		}
	}
	return inodes, nil
}

// ownsSocket reports whether one of the inodes is a listening TCP socket or a
// bound UDP socket on the port, in either address family.
func ownsSocket(procRoot string, want listener, inodes map[string]bool) (bool, error) {
	for _, table := range []string{want.Network, want.Network + "6"} {
		data, err := os.ReadFile(filepath.Join(procRoot, "net", table))
		if errors.Is(err, os.ErrNotExist) {
			continue // the address family is disabled on this host
		}
		if err != nil {
			return false, err
		}
		for _, line := range strings.Split(string(data), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 || !inodes[fields[9]] {
				continue
			}
			_, portHex, _ := strings.Cut(fields[1], ":")
			port, err := strconv.ParseUint(portHex, 16, 16)
			if err != nil || uint16(port) != want.Port {
				continue
			}
			if want.Network == "udp" || fields[3] == "0A" {
				return true, nil
			}
		}
	}
	return false, nil
}

func rollback(ctx context.Context, options Options, previous string, changes []serviceChange) error {
	// Stop every potentially activated replacement before restoring old ports.
	// Continue after individual errors so one failed unit cannot prevent recovery
	// of all remaining units. Never touch an unchanged instance.
	var failures []error
	for _, change := range changes {
		if change.after {
			failures = append(failures, serviceCommand(ctx, options, "stop", change.unit))
		}
	}
	if previous != "" {
		if err := switchCurrent(options.RuntimeRoot, previous); err != nil {
			return errors.Join(append(failures, fmt.Errorf("restore previous release: %w", err))...)
		}
	} else if err := os.Remove(filepath.Join(options.RuntimeRoot, "current")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(append(failures, err)...)
	}
	var restored []startedUnit
	for _, change := range changes {
		if !change.before {
			failures = append(failures, serviceCommand(ctx, options, "disable", change.unit))
			continue
		}
		unit, err := startUnit(ctx, options, change.unit)
		failures = append(failures, err)
		if err == nil {
			restored = append(restored, unit)
		}
	}
	listeners, err := releaseListeners(previous)
	failures = append(failures, err, verifyStarted(ctx, options, restored, listeners))
	return errors.Join(failures...)
}

// releaseListeners reads the sockets a release expects each unit to own.
// Releases written before listeners were recorded expect none.
func releaseListeners(release string) (map[string][]listener, error) {
	if release == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(release, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Listeners map[string][]listener `json:"listeners"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("read release manifest: %w", err)
	}
	return manifest.Listeners, nil
}

// restartUnits restarts every unit, continuing past failures, and then
// verifies the ones that started.
func restartUnits(ctx context.Context, options Options, units []string, listeners map[string][]listener) error {
	var failures []error
	var started []startedUnit
	for _, unit := range units {
		if err := serviceCommand(ctx, options, "restart", unit); err != nil {
			failures = append(failures, err)
			continue
		}
		state, err := showUnit(ctx, options, unit)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		started = append(started, startedUnit{unit: unit, pid: state.pid})
	}
	return errors.Join(append(failures, verifyStarted(ctx, options, started, listeners))...)
}
