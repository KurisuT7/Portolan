package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/KurisuT7/Portolan/internal/agentproto"
	"github.com/KurisuT7/Portolan/internal/configgen"
	"github.com/KurisuT7/Portolan/internal/model"
)

var safeID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,96}$`)

type Options struct {
	RuntimeRoot        string
	SingBoxBinary      string
	RealmBinary        string
	SystemctlBinary    string
	SingBoxService     string
	RealmServicePrefix string
	ProcRoot           string
	SkipServiceActions bool
	KeepReleases       int
	RunCommand         func(context.Context, string, ...string) (string, error)
}

type Result struct {
	Revision       int64    `json:"revision"`
	NodeCount      int      `json:"node_count"`
	ForwardCount   int      `json:"forward_count"`
	RealmInstances []string `json:"realm_instances"`
	Reloaded       bool     `json:"reloaded"`
}

func Apply(ctx context.Context, payload agentproto.SyncPayload, options Options) (Result, error) {
	if err := defaultsAndValidate(&options); err != nil {
		return Result{}, err
	}
	if err := validateDesiredState(payload); err != nil {
		return Result{}, err
	}
	releasesRoot := filepath.Join(options.RuntimeRoot, "releases")
	if err := os.MkdirAll(releasesRoot, 0o750); err != nil {
		return Result{}, err
	}
	revisionName := strconv.FormatInt(payload.Revision, 10)
	staging := filepath.Join(releasesRoot, revisionName+".tmp")
	if err := ensureManagedChild(releasesRoot, staging); err != nil {
		return Result{}, err
	}
	if err := os.RemoveAll(staging); err != nil {
		return Result{}, err
	}
	if err := writeRelease(staging, payload, options); err != nil {
		_ = os.RemoveAll(staging)
		return Result{}, err
	}
	if output, err := options.RunCommand(ctx, options.SingBoxBinary, "check",
		"-c", filepath.Join(staging, "sing-box", "00-base.json"),
		"-C", filepath.Join(staging, "sing-box", "conf.d")); err != nil {
		_ = os.RemoveAll(staging)
		return Result{}, fmt.Errorf("sing-box rejected generated configuration: %w: %s", err, output)
	}
	realmInstances := realmInstanceIDs(payload.Forwards)
	if len(realmInstances) > 0 {
		if _, err := os.Stat(options.RealmBinary); err != nil {
			_ = os.RemoveAll(staging)
			return Result{}, fmt.Errorf("realm engine requested but binary is unavailable: %w", err)
		}
	}
	release := filepath.Join(releasesRoot, revisionName)
	if err := os.Rename(staging, release); err != nil {
		return Result{}, err
	}
	previousTarget, _ := filepath.EvalSymlinks(filepath.Join(options.RuntimeRoot, "current"))
	plan, err := planServices(options, previousTarget, release)
	if err != nil {
		return Result{}, err
	}
	if err := switchCurrent(options.RuntimeRoot, release); err != nil {
		return Result{}, err
	}
	result := Result{Revision: payload.Revision, NodeCount: len(payload.Nodes), ForwardCount: len(payload.Forwards), RealmInstances: realmInstances}
	if !options.SkipServiceActions {
		if err := activateServices(ctx, options, plan, expectedListeners(options, payload)); err != nil {
			// A cancelled apply still needs a bounded opportunity to restore service.
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			rollbackErr := rollback(rollbackCtx, options, previousTarget, plan)
			return Result{}, errors.Join(fmt.Errorf("activate generated configuration: %w", err), rollbackErr)
		}
		result.Reloaded = len(plan) > 0
	}
	pruneReleases(releasesRoot, options.KeepReleases, release, previousTarget)
	return result, nil
}

func defaultsAndValidate(options *Options) error {
	options.RuntimeRoot = filepath.Clean(options.RuntimeRoot)
	if options.RuntimeRoot == "." || options.RuntimeRoot == string(filepath.Separator) || options.RuntimeRoot == "" {
		return errors.New("runtime root must be a dedicated absolute directory")
	}
	if !filepath.IsAbs(options.RuntimeRoot) {
		return errors.New("runtime root must be absolute")
	}
	if options.SingBoxBinary == "" {
		options.SingBoxBinary = "/usr/local/lib/portolan/sing-box"
	}
	if options.RealmBinary == "" {
		options.RealmBinary = "/usr/local/lib/portolan/realm"
	}
	if options.SystemctlBinary == "" {
		options.SystemctlBinary = "/usr/bin/systemctl"
	}
	if options.SingBoxService == "" {
		options.SingBoxService = "portolan-sing-box.service"
	}
	if options.RealmServicePrefix == "" {
		options.RealmServicePrefix = "portolan-realm@"
	}
	if options.ProcRoot == "" {
		options.ProcRoot = "/proc"
	}
	if options.KeepReleases < 2 {
		options.KeepReleases = 4
	}
	if options.RunCommand == nil {
		options.RunCommand = command
	}
	return nil
}

func validateDesiredState(payload agentproto.SyncPayload) error {
	if payload.Revision <= 0 {
		return errors.New("desired-state revision is invalid")
	}
	ports := map[uint16]string{}
	for _, node := range payload.Nodes {
		if !safeID.MatchString(node.ID) {
			return fmt.Errorf("node ID %q is unsafe", node.ID)
		}
		if err := node.Validate(); err != nil {
			return fmt.Errorf("node %s: %w", node.ID, err)
		}
		if node.Enabled {
			if owner, exists := ports[node.ListenPort]; exists {
				return fmt.Errorf("listen port %d is shared by %s and node %s", node.ListenPort, owner, node.ID)
			}
			ports[node.ListenPort] = "node " + node.ID
		}
	}
	for _, forward := range payload.Forwards {
		if !safeID.MatchString(forward.ID) {
			return fmt.Errorf("forward ID %q is unsafe", forward.ID)
		}
		if err := forward.Validate(); err != nil {
			return fmt.Errorf("forward %s: %w", forward.ID, err)
		}
		if forward.Enabled {
			if owner, exists := ports[forward.ListenPort]; exists {
				return fmt.Errorf("listen port %d is shared by %s and forward %s", forward.ListenPort, owner, forward.ID)
			}
			ports[forward.ListenPort] = "forward " + forward.ID
		}
	}
	return nil
}

func writeRelease(root string, payload agentproto.SyncPayload, options Options) error {
	singBoxDir := filepath.Join(root, "sing-box")
	confDir := filepath.Join(singBoxDir, "conf.d")
	realmDir := filepath.Join(root, "realm")
	for _, path := range []string{confDir, realmDir} {
		if err := os.MkdirAll(path, 0o750); err != nil {
			return err
		}
	}
	base, err := configgen.BaseConfig()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(singBoxDir, "00-base.json"), append(base, '\n'), 0o640); err != nil {
		return err
	}
	for _, node := range payload.Nodes {
		if !node.Enabled {
			continue
		}
		data, err := configgen.NodeFragment(node)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(confDir, "10-node-"+node.ID+".json"), append(data, '\n'), 0o640); err != nil {
			return err
		}
	}
	for _, forward := range payload.Forwards {
		if !forward.Enabled {
			continue
		}
		switch forward.Engine {
		case model.ForwardSingBox:
			data, err := configgen.ForwardFragment(forward)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(confDir, "20-forward-"+forward.ID+".json"), append(data, '\n'), 0o640); err != nil {
				return err
			}
		case model.ForwardRealm:
			data, err := configgen.RealmConfig(forward)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(realmDir, forward.ID+".toml"), data, 0o640); err != nil {
				return err
			}
		}
	}
	manifest, _ := json.MarshalIndent(map[string]any{"revision": payload.Revision, "nodes": len(payload.Nodes), "forwards": len(payload.Forwards),
		"listeners": expectedListeners(options, payload), "created_at": time.Now().UTC()}, "", "  ")
	return os.WriteFile(filepath.Join(root, "manifest.json"), append(manifest, '\n'), 0o640)
}

func switchCurrent(runtimeRoot, release string) error {
	current := filepath.Join(runtimeRoot, "current")
	next := filepath.Join(runtimeRoot, ".current-next")
	if err := os.Remove(next); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Symlink(release, next); err != nil {
		return err
	}
	// Rename replaces the symlink atomically on the Linux Agent host. There is
	// never a window where current is missing between two rename operations.
	if err := os.Rename(next, current); err != nil {
		_ = os.Remove(next)
		return err
	}
	return nil
}

func realmInstanceIDs(forwards []model.Forward) []string {
	var ids []string
	for _, forward := range forwards {
		if forward.Enabled && forward.Engine == model.ForwardRealm {
			ids = append(ids, forward.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

func command(ctx context.Context, name string, arguments ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, arguments...)
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func pruneReleases(root string, keep int, protected ...string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	type releaseEntry struct {
		path string
		info fs.FileInfo
	}
	var releases []releaseEntry
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		info, err := entry.Info()
		if err == nil {
			releases = append(releases, releaseEntry{path: filepath.Join(root, entry.Name()), info: info})
		}
	}
	slices.SortFunc(releases, func(a, b releaseEntry) int { return b.info.ModTime().Compare(a.info.ModTime()) })
	for _, entry := range releases[minimum(keep, len(releases)):] {
		isProtected := false
		for _, path := range protected {
			isProtected = isProtected || samePath(entry.path, path)
		}
		if !isProtected {
			_ = os.RemoveAll(entry.path)
		}
	}
}

func ensureManagedChild(parent, child string) error {
	relative, err := filepath.Rel(parent, child)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return errors.New("refusing to modify a path outside the releases directory")
	}
	return nil
}

func samePath(a, b string) bool {
	return b != "" && filepath.Clean(a) == filepath.Clean(b)
}

func minimum(a, b int) int {
	if a < b {
		return a
	}
	return b
}
