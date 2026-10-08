package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/KurisuT7/Portolan/internal/agentproto"
	"github.com/KurisuT7/Portolan/internal/buildinfo"
)

// ErrReplaced reports that the Agent binary was replaced. The process exits
// so that systemd starts the new version, which completes the update job.
var ErrReplaced = errors.New("the Agent binary was replaced")

const (
	agentService        = "portolan-agent.service"
	rollbackUnit        = "portolan-agent-rollback"
	rollbackDelay       = 3 * time.Minute
	updateMarkerName    = "agent-update.json"
	maxAgentBinaryBytes = 256 << 20
	updatePending       = "pending"
	updateRolledBack    = "rolled-back"
)

// Fixed summaries reported to the panel; command output stays in the local log.
const (
	updateInvalid     = "Agent 更新请求无效。"
	updateDownload    = "新版本 Agent 下载或校验失败，未替换。"
	updateCannotRun   = "新版本 Agent 无法在这台服务器上运行，未替换。"
	updateCannotReach = "新版本 Agent 无法连接面板，未替换。"
	updateFailed      = "Agent 更新失败，未替换。"
	updateInterrupted = "Agent 更新中断，未替换。"
	updateReverted    = "新版本 Agent 没有正常连接面板，已换回原版本。"
)

// agentUpdate is kept next to the Agent configuration while an update is in
// flight, so that the next Agent process can complete the update job.
type agentUpdate struct {
	JobID   string `json:"job_id"`
	Version string `json:"version"`
	Binary  string `json:"binary"`
	State   string `json:"state"`
}

func updateResult(version, message string) string {
	data, _ := json.Marshal(map[string]string{"version": version, "message": message})
	return string(data)
}

// updateAgent installs the Agent release named by the job. The new binary must
// run on this host and reach the panel with this Agent's credentials before it
// replaces the current one. A transient systemd timer restores the previous
// binary unless the new Agent confirms within rollbackDelay.
func (c *Client) updateAgent(ctx context.Context, job agentproto.Job) (string, error) {
	var payload agentproto.AgentUpdatePayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.Version == "" {
		return updateResult("", updateInvalid), errors.New("invalid Agent update request")
	}
	result := func(message string) string { return updateResult(payload.Version, message) }
	if payload.Version == buildinfo.Version {
		return result(""), nil
	}
	if c.config.Path == "" || c.executable == "" {
		return result(updateFailed), errors.New("the Agent configuration or executable path is unknown")
	}
	binary := c.executable
	staged := filepath.Join(filepath.Dir(binary), "."+filepath.Base(binary)+".new")
	defer os.Remove(staged)
	if err := c.downloadAgent(ctx, payload, staged); err != nil {
		return result(updateDownload), err
	}
	if output, err := c.run(ctx, staged, "version"); err != nil || output != payload.Version {
		return result(updateCannotRun), fmt.Errorf("new Agent reports %q: %v", output, err)
	}
	if output, err := c.run(ctx, staged, "check", "--config", c.config.Path); err != nil {
		return result(updateCannotReach), fmt.Errorf("new Agent cannot reach the panel: %w: %s", err, output)
	}
	c.replacing.Store(true)
	marker := agentUpdate{JobID: job.ID, Version: payload.Version, Binary: binary, State: updatePending}
	if err := writeUpdateMarker(c.updateMarkerPath(), marker); err != nil {
		c.replacing.Store(false)
		return result(updateFailed), err
	}
	previous := binary + ".previous"
	undo := func() {
		c.disarmRollback(context.WithoutCancel(ctx))
		_ = os.Remove(previous)
		_ = os.Remove(c.updateMarkerPath())
		c.replacing.Store(false)
	}
	if err := os.Remove(previous); err != nil && !errors.Is(err, os.ErrNotExist) {
		undo()
		return result(updateFailed), err
	}
	if err := os.Link(binary, previous); err != nil {
		undo()
		return result(updateFailed), err
	}
	if err := c.armRollback(ctx, previous); err != nil {
		undo()
		return result(updateFailed), err
	}
	if err := os.Rename(staged, binary); err != nil {
		undo()
		return result(updateFailed), err
	}
	slog.Info("Agent binary replaced; restarting into the new version", "version", payload.Version)
	return "", ErrReplaced
}

func (c *Client) downloadAgent(ctx context.Context, payload agentproto.AgentUpdatePayload, staged string) error {
	expected := payload.SHA256[runtime.GOARCH]
	if expected == "" {
		return fmt.Errorf("no Agent binary for %s", runtime.GOARCH)
	}
	ctx, cancel := context.WithTimeout(ctx, coreDownloadTimeout)
	defer cancel()
	request, err := c.request(ctx, http.MethodGet, "/api/v1/agent/binary/"+runtime.GOARCH, nil)
	if err != nil {
		return err
	}
	// The shared client's timeout is sized for API calls, not binaries.
	response, err := (&http.Client{Transport: c.http.Transport}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return responseError(response)
	}
	file, err := os.OpenFile(staged, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, maxAgentBinaryBytes))
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return err
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != expected {
		return fmt.Errorf("Agent binary SHA-256 %s does not match %s", actual, expected)
	}
	return os.Chmod(staged, 0o755)
}

// armRollback schedules `<previous> rollback` outside this service, so that it
// still runs when the new Agent fails to start.
func (c *Client) armRollback(ctx context.Context, previous string) error {
	if c.config.SkipServiceActions {
		return nil
	}
	c.disarmRollback(ctx)
	systemdRun := filepath.Join(filepath.Dir(c.systemctl()), "systemd-run")
	output, err := c.run(ctx, systemdRun, "--unit", rollbackUnit, "--on-active", strconv.Itoa(int(rollbackDelay.Seconds())),
		"--timer-property", "AccuracySec=1s", previous, "rollback", "--config", c.config.Path)
	if err != nil {
		return fmt.Errorf("schedule Agent rollback: %w: %s", err, output)
	}
	return nil
}

func (c *Client) disarmRollback(ctx context.Context) {
	if c.config.SkipServiceActions {
		return
	}
	_, _ = c.run(ctx, c.systemctl(), "stop", rollbackUnit+".timer", rollbackUnit+".service")
	_, _ = c.run(ctx, c.systemctl(), "reset-failed", rollbackUnit+".timer", rollbackUnit+".service")
}

func (c *Client) systemctl() string {
	if c.config.SystemctlBinary != "" {
		return c.config.SystemctlBinary
	}
	return "/usr/bin/systemctl"
}

func (c *Client) updateMarkerPath() string {
	return filepath.Join(filepath.Dir(c.config.Path), updateMarkerName)
}

// finishUpdate completes an update job left by the previous Agent process
// once this process has reached the panel. It reports whether nothing is left
// to do.
func (c *Client) finishUpdate(ctx context.Context) bool {
	if c.config.Path == "" || c.replacing.Load() {
		return true
	}
	path := c.updateMarkerPath()
	marker, err := readUpdateMarker(path)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		slog.Warn("unreadable Agent update record removed", "error", err)
		_ = os.Remove(path)
		return true
	}
	success, message := false, updateInterrupted
	switch {
	case marker.State == updateRolledBack:
		message = updateReverted
	case marker.Version == buildinfo.Version:
		success, message = true, ""
	}
	if marker.State == updatePending {
		c.disarmRollback(ctx)
		_ = os.Remove(marker.Binary + ".previous")
	}
	if err := c.completeJob(ctx, marker.JobID, success, updateResult(marker.Version, message)); err != nil {
		var rejected *panelError
		if !errors.As(err, &rejected) || rejected.Status >= 500 {
			slog.Warn("Agent update completion report failed", "job", marker.JobID, "error", err)
			return false
		}
	}
	_ = os.Remove(path)
	return true
}

// Check confirms that the configuration loads and the panel accepts this
// Agent's credentials. A new binary runs it before it replaces the old one.
func (c *Client) Check(ctx context.Context) error {
	_, err := c.listForwards(ctx)
	return err
}

// Rollback restores the binary saved by an update the new Agent did not
// confirm and restarts the Agent. It runs from the transient rollback timer.
func Rollback(ctx context.Context, configPath string) error {
	config, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	path := filepath.Join(filepath.Dir(configPath), updateMarkerName)
	marker, err := readUpdateMarker(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && marker.State != updatePending) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.Rename(marker.Binary+".previous", marker.Binary); err != nil {
		return err
	}
	marker.State = updateRolledBack
	if err := writeUpdateMarker(path, marker); err != nil {
		return err
	}
	systemctl := config.SystemctlBinary
	if systemctl == "" {
		systemctl = "/usr/bin/systemctl"
	}
	output, err := runCommand(ctx, systemctl, "restart", agentService)
	if err != nil {
		return fmt.Errorf("restart the Agent: %w: %s", err, output)
	}
	return nil
}

func readUpdateMarker(path string) (agentUpdate, error) {
	var marker agentUpdate
	data, err := os.ReadFile(path)
	if err != nil {
		return marker, err
	}
	if err := json.Unmarshal(data, &marker); err != nil {
		return marker, err
	}
	if marker.JobID == "" || !filepath.IsAbs(marker.Binary) || (marker.State != updatePending && marker.State != updateRolledBack) {
		return marker, errors.New("incomplete Agent update record")
	}
	return marker, nil
}

func writeUpdateMarker(path string, marker agentUpdate) error {
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func runCommand(ctx context.Context, name string, arguments ...string) (string, error) {
	output, err := exec.CommandContext(ctx, name, arguments...).CombinedOutput()
	return strings.TrimSpace(string(output)), err
}
