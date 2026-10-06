package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KurisuT7/portolan/internal/agentproto"
	applyconfig "github.com/KurisuT7/portolan/internal/apply"
	"github.com/KurisuT7/portolan/internal/buildinfo"
	"github.com/KurisuT7/portolan/internal/discovery"
	"github.com/KurisuT7/portolan/internal/model"
)

type Config struct {
	PanelURL           string        `json:"panel_url"`
	ServerID           string        `json:"server_id"`
	AgentToken         string        `json:"agent_token"`
	RuntimeRoot        string        `json:"runtime_root"`
	SingBoxBinary      string        `json:"sing_box_binary"`
	RealmBinary        string        `json:"realm_binary"`
	SystemctlBinary    string        `json:"systemctl_binary"`
	PollInterval       time.Duration `json:"poll_interval"`
	AllowInsecureHTTP  bool          `json:"allow_insecure_http"`
	SkipServiceActions bool          `json:"skip_service_actions,omitempty"`
}

type Client struct {
	config             Config
	http               *http.Client
	forwardsMu         sync.Mutex
	forwards           []model.Forward
	forwardsFreshUntil time.Time
	// statusMu keeps a runtime report from being read before a job and
	// delivered after that job's completion report.
	statusMu sync.Mutex
}

type Enrollment struct {
	ServerID   string `json:"server_id"`
	AgentToken string `json:"agent_token"`
}

const (
	publicAddressesHeader = "X-Portolan-Public-Addresses"
	egressFamiliesHeader  = "X-Portolan-Egress-Families"
	agentJobLongPoll      = 60 * time.Second
	forwardListFreshness  = time.Minute
	discoveryInterval     = 10 * time.Minute
	discoveryRetry        = time.Minute
	statusInterval        = 30 * time.Second
	coreDownloadTimeout   = 15 * time.Minute
	maxCoreArchiveBytes   = 512 << 20
)

type networkReport struct {
	PublicAddresses []string
	EgressFamilies  []string
}

func New(config Config) (*Client, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Client{config: config, http: &http.Client{Timeout: 75 * time.Second}}, nil
}

func Enroll(ctx context.Context, panelURL, enrollmentToken string, allowInsecureHTTP bool) (Enrollment, error) {
	if err := validatePanelURL(panelURL, allowInsecureHTTP); err != nil {
		return Enrollment{}, err
	}
	network := localNetworkReport()
	body, _ := json.Marshal(struct {
		EnrollmentToken string   `json:"enrollment_token"`
		PublicAddresses []string `json:"public_addresses"`
		EgressFamilies  []string `json:"egress_families"`
	}{
		EnrollmentToken: enrollmentToken,
		PublicAddresses: network.PublicAddresses,
		EgressFamilies:  network.EgressFamilies,
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(panelURL, "/")+"/api/v1/agent/enroll", bytes.NewReader(body))
	if err != nil {
		return Enrollment{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "portolan-agent/"+buildinfo.Version)
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(request)
	if err != nil {
		return Enrollment{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return Enrollment{}, responseError(response)
	}
	var enrollment Enrollment
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&enrollment); err != nil {
		return Enrollment{}, err
	}
	if enrollment.ServerID == "" || enrollment.AgentToken == "" {
		return Enrollment{}, errors.New("panel returned incomplete enrollment credentials")
	}
	return enrollment, nil
}

func localNetworkReport() networkReport {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return networkReport{}
	}
	return networkReportFromAddresses(addresses)
}

func networkReportFromAddresses(addresses []net.Addr) networkReport {
	unique := make(map[string]struct{})
	publicAddresses := make([]string, 0, len(addresses))
	egressIPv4 := false
	egressIPv6 := false
	for _, address := range addresses {
		host, _, splitErr := net.ParseCIDR(address.String())
		if splitErr != nil {
			host = net.ParseIP(address.String())
		}
		if host == nil || host.IsUnspecified() || host.IsLoopback() || host.IsLinkLocalUnicast() || host.IsMulticast() {
			continue
		}
		value := host.String()
		if host.To4() != nil {
			egressIPv4 = true
		} else {
			egressIPv6 = true
		}
		if !model.IsPublicRoutableIP(value) {
			continue
		}
		if _, exists := unique[value]; exists {
			continue
		}
		unique[value] = struct{}{}
		publicAddresses = append(publicAddresses, value)
	}
	sort.SliceStable(publicAddresses, func(left, right int) bool {
		return (net.ParseIP(publicAddresses[left]).To4() != nil) && net.ParseIP(publicAddresses[right]).To4() == nil
	})
	egressFamilies := make([]string, 0, 2)
	if egressIPv4 {
		egressFamilies = append(egressFamilies, "ipv4")
	}
	if egressIPv6 {
		egressFamilies = append(egressFamilies, "ipv6")
	}
	return networkReport{PublicAddresses: publicAddresses, EgressFamilies: egressFamilies}
}

func (c Config) Validate() error {
	if c.ServerID == "" || len(c.AgentToken) < 32 {
		return errors.New("server ID and agent token are required")
	}
	if err := validatePanelURL(c.PanelURL, c.AllowInsecureHTTP); err != nil {
		return err
	}
	if !filepath.IsAbs(c.RuntimeRoot) {
		return errors.New("runtime root must be an absolute path")
	}
	return nil
}

func SaveConfig(path string, config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, err
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 15 * time.Second
	}
	return config, config.Validate()
}

func (c *Client) Run(ctx context.Context, once bool) error {
	if once {
		return c.runOnce(ctx)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, 4)
	go func() { results <- c.runJobLoop(runCtx) }()
	go func() { results <- c.runProbeLoop(runCtx) }()
	go func() { results <- c.runDiscoveryLoop(runCtx) }()
	go func() { results <- c.runStatusLoop(runCtx) }()

	errorsSeen := []error{<-results}
	cancel()
	errorsSeen = append(errorsSeen, <-results, <-results, <-results)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, err := range errorsSeen {
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	return errorsSeen[0]
}

func (c *Client) runOnce(ctx context.Context) error {
	if err := c.discoverAndReport(ctx); err != nil {
		slog.Warn("existing node discovery report failed", "error", err)
	}
	job, err := c.nextJob(ctx, 0)
	if err != nil {
		return err
	}
	if job != nil {
		result, jobErr := c.executeJob(ctx, *job)
		if err := c.completeJob(ctx, job.ID, jobErr == nil, result); err != nil {
			return err
		}
		if jobErr != nil {
			return jobErr
		}
	}
	if err := c.reportStatus(ctx); err != nil {
		return err
	}
	return c.probeAndReport(ctx)
}

func (c *Client) runJobLoop(ctx context.Context) error {
	backoff := time.Second
	for {
		job, err := c.nextJob(ctx, agentJobLongPoll)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Warn("Agent job poll failed", "error", err)
			if !wait(ctx, backoff) {
				return ctx.Err()
			}
			backoff = minimumDuration(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second
		if job != nil {
			c.statusMu.Lock()
			result, jobErr := c.executeJob(ctx, *job)
			if err := c.completeJob(ctx, job.ID, jobErr == nil, result); err != nil {
				slog.Warn("Agent job completion report failed", "job", job.ID, "error", err)
			}
			c.statusMu.Unlock()
			if jobErr != nil {
				slog.Warn("Agent job failed", "job", job.ID, "type", job.Type, "error", jobErr)
			}
			if job.Type != "probe" {
				if err := c.reportStatus(ctx); err != nil && ctx.Err() == nil {
					slog.Warn("runtime status report failed", "error", err)
				}
			}
		}
	}
}

func (c *Client) runStatusLoop(ctx context.Context) error {
	for {
		if err := c.reportStatus(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("runtime status report failed", "error", err)
		}
		if !wait(ctx, statusInterval) {
			return ctx.Err()
		}
	}
}

func (c *Client) reportStatus(ctx context.Context) error {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	status, err := applyconfig.Inspect(ctx, c.applyOptions())
	if err != nil {
		return err
	}
	status.AgentVersion = buildinfo.Version
	body, err := json.Marshal(status)
	if err != nil {
		return err
	}
	request, err := c.request(ctx, http.MethodPost, "/api/v1/agent/status", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return responseError(response)
	}
	return nil
}

func (c *Client) applyOptions() applyconfig.Options {
	return applyconfig.Options{
		RuntimeRoot: c.config.RuntimeRoot, SingBoxBinary: c.config.SingBoxBinary, RealmBinary: c.config.RealmBinary,
		SystemctlBinary: c.config.SystemctlBinary, SkipServiceActions: c.config.SkipServiceActions,
	}
}

func (c *Client) runProbeLoop(ctx context.Context) error {
	for {
		if err := c.probeAndReport(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("periodic forward probe failed", "error", err)
		}
		if !wait(ctx, c.config.PollInterval) {
			return ctx.Err()
		}
	}
}

func (c *Client) runDiscoveryLoop(ctx context.Context) error {
	for {
		next := discoveryInterval
		if err := c.discoverAndReport(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Warn("existing node discovery report failed", "error", err)
			next = discoveryRetry
		}
		if !wait(ctx, next) {
			return ctx.Err()
		}
	}
}

func (c *Client) discoverAndReport(ctx context.Context) error {
	report := discovery.ScanSystem(ctx)
	if len(report.Warnings) > 0 {
		slog.Debug("existing node discovery completed with ignored configurations", "warnings", len(report.Warnings))
	}
	body, err := json.Marshal(map[string]any{"items": report.Items})
	if err != nil {
		return err
	}
	request, err := c.request(ctx, http.MethodPost, "/api/v1/agent/discovered-nodes", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return responseError(response)
	}
	return nil
}

func (c *Client) probeAndReport(ctx context.Context) error {
	forwards, err := c.periodicForwards(ctx)
	if err != nil {
		return err
	}
	enabled := make([]model.Forward, 0, len(forwards))
	for _, forward := range forwards {
		if forward.Enabled {
			enabled = append(enabled, forward)
		}
	}
	forwards = enabled
	if len(forwards) == 0 {
		return nil
	}
	results := make([]model.ForwardProbe, len(forwards))
	var waitGroup sync.WaitGroup
	for index, forward := range forwards {
		waitGroup.Add(1)
		go func(index int, forward model.Forward) {
			defer waitGroup.Done()
			results[index] = probeForward(ctx, forward)
		}(index, forward)
	}
	waitGroup.Wait()
	return c.reportProbes(ctx, results)
}

func (c *Client) listForwards(ctx context.Context) ([]model.Forward, error) {
	request, err := c.request(ctx, http.MethodGet, "/api/v1/agent/forwards", nil)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, responseError(response)
	}
	var payload struct {
		Items []model.Forward `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	c.rememberForwards(payload.Items)
	return payload.Items, nil
}

func (c *Client) periodicForwards(ctx context.Context) ([]model.Forward, error) {
	c.forwardsMu.Lock()
	if time.Now().Before(c.forwardsFreshUntil) {
		forwards := append([]model.Forward(nil), c.forwards...)
		c.forwardsMu.Unlock()
		return forwards, nil
	}
	c.forwardsMu.Unlock()
	return c.listForwards(ctx)
}

func (c *Client) rememberForwards(forwards []model.Forward) {
	c.forwardsMu.Lock()
	defer c.forwardsMu.Unlock()
	c.forwards = append([]model.Forward(nil), forwards...)
	c.forwardsFreshUntil = time.Now().Add(forwardListFreshness)
}

func (c *Client) reportProbes(ctx context.Context, results []model.ForwardProbe) error {
	body, _ := json.Marshal(map[string]any{"items": results})
	report, err := c.request(ctx, http.MethodPost, "/api/v1/agent/forward-probes", bytes.NewReader(body))
	if err != nil {
		return err
	}
	report.Header.Set("Content-Type", "application/json")
	reportResponse, err := c.http.Do(report)
	if err != nil {
		return err
	}
	defer reportResponse.Body.Close()
	if reportResponse.StatusCode != http.StatusNoContent {
		return responseError(reportResponse)
	}
	return nil
}

func (c *Client) runImmediateProbe(ctx context.Context, forwardID string) (model.ForwardProbe, error) {
	forwards, err := c.listForwards(ctx)
	if err != nil {
		return model.ForwardProbe{}, err
	}
	for _, forward := range forwards {
		if forward.ID != forwardID {
			continue
		}
		probe := probeForward(ctx, forward)
		if err := c.reportProbes(ctx, []model.ForwardProbe{probe}); err != nil {
			return model.ForwardProbe{}, err
		}
		return probe, nil
	}
	return model.ForwardProbe{}, errors.New("requested forward is no longer assigned to this Agent")
}

func probeForward(ctx context.Context, forward model.Forward) model.ForwardProbe {
	probe := model.ForwardProbe{ForwardID: forward.ID, CheckedAt: time.Now().UTC(), Attempts: 3}
	if !containsNetwork(forward.Networks, "tcp") {
		probe.Status = "unsupported"
		probe.LastError = "generic UDP reachability requires an application-aware responder"
		return probe
	}
	address := net.JoinHostPort(model.NormalizeHost(forward.TargetHost), fmt.Sprint(forward.TargetPort))
	latencies := make([]float64, 0, probe.Attempts)
	for attempt := 0; attempt < probe.Attempts; attempt++ {
		started := time.Now()
		attemptCtx, cancel := context.WithTimeout(ctx, 1800*time.Millisecond)
		connection, err := (&net.Dialer{}).DialContext(attemptCtx, "tcp", address)
		cancel()
		if err != nil {
			probe.LastError = err.Error()
			continue
		}
		_ = connection.Close()
		probe.Successes++
		latencies = append(latencies, float64(time.Since(started).Microseconds())/1000)
	}
	probe.LossPct = 100 * float64(probe.Attempts-probe.Successes) / float64(probe.Attempts)
	if len(latencies) > 0 {
		sort.Float64s(latencies)
		probe.LatencyMS = roundMetric(latencies[len(latencies)/2])
		if len(latencies) > 1 {
			var total float64
			for index := 1; index < len(latencies); index++ {
				total += math.Abs(latencies[index] - latencies[index-1])
			}
			probe.JitterMS = roundMetric(total / float64(len(latencies)-1))
		}
	}
	switch {
	case probe.Successes == 0:
		probe.Status = "down"
	case probe.LossPct > 0 || probe.JitterMS > 30 || probe.LatencyMS > 500:
		probe.Status = "degraded"
	default:
		probe.Status = "stable"
	}
	return probe
}

func containsNetwork(networks []string, expected string) bool {
	for _, network := range networks {
		if network == expected {
			return true
		}
	}
	return false
}

func roundMetric(value float64) float64 { return math.Round(value*10) / 10 }

func (c *Client) nextJob(ctx context.Context, longPoll time.Duration) (*agentproto.Job, error) {
	waitSeconds := int(math.Ceil(longPoll.Seconds()))
	path := fmt.Sprintf("/api/v1/agent/jobs/next?wait=%d", waitSeconds)
	request, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, responseError(response)
	}
	var job agentproto.Job
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&job); err != nil {
		return nil, err
	}
	return &job, nil
}

func (c *Client) executeJob(ctx context.Context, job agentproto.Job) (string, error) {
	switch job.Type {
	case "sync":
		return c.applyJob(ctx, job)
	case "probe":
		var payload agentproto.ProbeJobPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.ForwardID == "" {
			return "invalid immediate probe request", errors.New("invalid immediate probe request")
		}
		probe, err := c.runImmediateProbe(ctx, payload.ForwardID)
		if err != nil {
			return err.Error(), err
		}
		result, _ := json.Marshal(probe)
		return string(result), nil
	case model.CoreSingBox.JobType():
		return c.updateCore(ctx, model.CoreSingBox, job)
	case model.CoreRealm.JobType():
		return c.updateCore(ctx, model.CoreRealm, job)
	default:
		return "unsupported job type", fmt.Errorf("unsupported job type %q", job.Type)
	}
}

func (c *Client) updateCore(ctx context.Context, core model.Core, job agentproto.Job) (string, error) {
	var payload agentproto.CoreUpdatePayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return coreResult(core, "", "核心更新请求无效。"), err
	}
	result := func(message string) string { return coreResult(core, payload.Version, message) }
	archive, err := c.downloadCore(ctx, core, payload)
	if err != nil {
		return result("核心文件下载或校验失败，未替换。"), err
	}
	defer os.Remove(archive)
	if err := applyconfig.UpdateCore(ctx, core, archive, c.applyOptions()); err != nil {
		// Core output may contain protocol secrets; only a fixed summary is reported.
		message := "核心更新失败，请检查该服务器的 Agent 日志。"
		switch {
		case errors.Is(err, applyconfig.ErrCoreRollbackFailed):
			message = "新核心没有正常运行，换回原版本时也出错，请立即检查服务器。"
		case errors.Is(err, applyconfig.ErrCoreRestart):
			message = "新核心没有正常运行，已换回原版本。"
		case errors.Is(err, applyconfig.ErrCoreRejected):
			message = "新版本 sing-box 不接受当前配置，未替换。"
		case errors.Is(err, applyconfig.ErrCoreArchive):
			message = "核心文件下载或校验失败，未替换。"
		}
		return result(message), err
	}
	return result(""), nil
}

func coreResult(core model.Core, version, message string) string {
	data, _ := json.Marshal(map[string]string{"core": string(core), "version": version, "message": message})
	return string(data)
}

// downloadCore fetches the release archive for this Agent's architecture from
// the panel and checks it against the digest in the job.
func (c *Client) downloadCore(ctx context.Context, core model.Core, payload agentproto.CoreUpdatePayload) (string, error) {
	expected := payload.SHA256[runtime.GOARCH]
	if expected == "" {
		return "", fmt.Errorf("no %s archive for %s", core, runtime.GOARCH)
	}
	ctx, cancel := context.WithTimeout(ctx, coreDownloadTimeout)
	defer cancel()
	request, err := c.request(ctx, http.MethodGet, "/api/v1/agent/cores/"+url.PathEscape(string(core))+"/"+
		url.PathEscape(payload.Version)+"/"+runtime.GOARCH, nil)
	if err != nil {
		return "", err
	}
	// The shared client's timeout is sized for API calls, not archives.
	response, err := (&http.Client{Transport: c.http.Transport}).Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", responseError(response)
	}
	file, err := os.CreateTemp(c.config.RuntimeRoot, ".core-*.tar.gz")
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, maxCoreArchiveBytes))
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		os.Remove(file.Name())
		return "", err
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != expected {
		os.Remove(file.Name())
		return "", fmt.Errorf("%s archive SHA-256 %s does not match %s", core, actual, expected)
	}
	return file.Name(), nil
}

func (c *Client) applyJob(ctx context.Context, job agentproto.Job) (string, error) {
	var payload agentproto.SyncPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return "invalid desired state", err
	}
	result, err := applyconfig.Apply(ctx, payload, c.applyOptions())
	if err != nil {
		// Core output may contain protocol secrets. Only a fixed, safe summary
		// crosses the control-plane boundary; the detailed error stays local.
		message := "配置未能应用，请检查该服务器的 Agent 日志。"
		switch {
		case strings.Contains(err.Error(), "sing-box rejected generated configuration"):
			message = "核心配置校验失败，未切换到新配置。"
		case strings.Contains(err.Error(), "is not listening on"):
			message = "新配置的端口没有监听成功，可能已被其他程序占用；Agent 已尝试恢复上一版配置。"
		case strings.Contains(err.Error(), "did not stay running"):
			message = "服务启动后没有保持运行；Agent 已尝试恢复上一版配置，请检查服务器日志。"
		case strings.Contains(err.Error(), "activate generated configuration"):
			message = "服务激活失败；Agent 已尝试恢复上一版配置，请检查服务器日志确认恢复结果。"
		case strings.Contains(err.Error(), "realm engine requested but binary is unavailable"):
			message = "服务器缺少可用的 Realm 引擎，未切换到新配置。"
		}
		data, _ := json.Marshal(map[string]any{"revision": payload.Revision, "message": message})
		return string(data), err
	}
	c.rememberForwards(payload.Forwards)
	data, _ := json.Marshal(result)
	return string(data), nil
}

func (c *Client) completeJob(ctx context.Context, jobID string, success bool, result string) error {
	body, _ := json.Marshal(map[string]any{"success": success, "result": result})
	request, err := c.request(ctx, http.MethodPost, "/api/v1/agent/jobs/"+url.PathEscape(jobID)+"/complete", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return responseError(response)
	}
	return nil
}

func (c *Client) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.config.PanelURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.config.AgentToken)
	request.Header.Set("X-Portolan-Server-ID", c.config.ServerID)
	request.Header.Set("User-Agent", "portolan-agent/"+buildinfo.Version)
	network := localNetworkReport()
	request.Header.Set(publicAddressesHeader, headerList(network.PublicAddresses))
	request.Header.Set(egressFamiliesHeader, headerList(network.EgressFamilies))
	return request, nil
}

func headerList(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ",")
}

func validatePanelURL(raw string, allowInsecureHTTP bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("panel URL is invalid")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	host := parsed.Hostname()
	if parsed.Scheme == "http" && allowInsecureHTTP && (host == "localhost" || host == "127.0.0.1" || host == "::1") {
		return nil
	}
	return errors.New("panel URL must use HTTPS; insecure HTTP is allowed only for loopback testing")
}

func responseError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Error != "" {
		return fmt.Errorf("panel returned %s: %s", response.Status, payload.Error)
	}
	return fmt.Errorf("panel returned %s", response.Status)
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func minimumDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
