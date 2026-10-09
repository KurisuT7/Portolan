// Package panelupdate finds the latest Portolan release and hands an update
// request to the root-owned updater that install-panel.sh sets up.
//
// The panel never replaces itself. It writes the requested version to a file
// in its state directory; portolan-panel-update.path starts
// portolan-panel-update.service, which runs the installed install-panel.sh as
// root and records the outcome in a status file the panel can only read.
package panelupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Repository publishes the releases the updater installs.
const Repository = "KurisuT7/Portolan"

// Paths used by a systemd installation; ops/systemd/portolan-panel-update.path
// watches RequestPath and install-panel.sh writes StatusPath.
const (
	RequestPath = "/var/lib/portolan-panel/update-request"
	StatusPath  = "/var/lib/portolan-panel-update/status.json"
)

const (
	checkTTL        = time.Hour
	failedCheckTTL  = 5 * time.Minute
	staleUpdate     = 30 * time.Minute
	pendingDeadline = 2 * time.Minute
)

var releasePattern = regexp.MustCompile(`^v(\d{1,4})\.(\d{1,4})\.(\d{1,4})$`)

// Newer reports whether release a follows release b. Versions that are not
// releases, such as "dev", are never newer and nothing is newer than them.
func Newer(a, b string) bool {
	left, right := releasePattern.FindStringSubmatch(a), releasePattern.FindStringSubmatch(b)
	if left == nil || right == nil {
		return false
	}
	for index := 1; index <= 3; index++ {
		x, _ := strconv.Atoi(left[index])
		y, _ := strconv.Atoi(right[index])
		if x != y {
			return x > y
		}
	}
	return false
}

// Releases reads the latest stable release from GitHub and caches the answer.
type Releases struct {
	API  string
	HTTP *http.Client

	mu      sync.Mutex
	latest  string
	err     error
	expires time.Time
}

func NewReleases() *Releases {
	return &Releases{API: "https://api.github.com", HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// Latest returns the tag of the latest stable release, such as v0.3.0.
func (r *Releases) Latest(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Now().Before(r.expires) {
		return r.latest, r.err
	}
	r.latest, r.err = r.fetch(ctx)
	if r.err != nil {
		r.expires = time.Now().Add(failedCheckTTL)
	} else {
		r.expires = time.Now().Add(checkTTL)
	}
	return r.latest, r.err
}

func (r *Releases) fetch(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.API+"/repos/"+Repository+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "portolan-panel")
	response, err := r.HTTP.Do(request)
	if err != nil {
		return "", errors.New("无法连接 GitHub 检查新版本")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("检查新版本时 GitHub 返回 %s", response.Status)
	}
	var release struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&release); err != nil {
		return "", errors.New("GitHub 返回的版本信息无法读取")
	}
	if !releasePattern.MatchString(release.Tag) {
		return "", fmt.Errorf("GitHub 上的最新版本 %q 不是发布版本号", release.Tag)
	}
	return release.Tag, nil
}

// Status is the record the updater writes. State is "running", "succeeded"
// or "failed"; From and Target are release tags.
type Status struct {
	State      string     `json:"state"`
	From       string     `json:"from"`
	Target     string     `json:"target"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Updater exchanges files with the root-owned updater.
type Updater struct {
	Request string
	Status  string
}

// Available reports whether the updater is installed: install-panel.sh
// creates the status directory together with the systemd units.
func (u Updater) Available() bool {
	info, err := os.Stat(filepath.Dir(u.Status))
	return err == nil && info.IsDir()
}

// State describes the update that is queued, running or last finished.
type State struct {
	// Pending is the version of a request the updater has not picked up yet.
	Pending     string
	RequestedAt time.Time
	// Last is the most recent update the updater ran, if any.
	Last *Status
}

// Busy reports whether an update is queued or running.
func (s State) Busy() bool {
	return s.Pending != "" || (s.Last != nil && s.Last.State == "running")
}

// Read returns the pending request and the last status as of now. A request
// the updater did not pick up within two minutes is dropped from Pending and
// reported as Unanswered, and a run that never finished is reported as failed,
// so a stopped updater cannot block later attempts.
func (u Updater) Read(now time.Time) (state State, unanswered bool, err error) {
	if data, readErr := os.ReadFile(u.Request); readErr == nil {
		if info, statErr := os.Stat(u.Request); statErr == nil {
			state.RequestedAt = info.ModTime()
		}
		if version := string(trimLine(data)); releasePattern.MatchString(version) {
			if now.Sub(state.RequestedAt) < pendingDeadline {
				state.Pending = version
			} else {
				unanswered = true
			}
		}
	} else if !errors.Is(readErr, fs.ErrNotExist) {
		return state, false, readErr
	}
	data, err := os.ReadFile(u.Status)
	if errors.Is(err, fs.ErrNotExist) {
		return state, unanswered, nil
	}
	if err != nil {
		return state, unanswered, err
	}
	var status Status
	if err := json.Unmarshal(data, &status); err != nil {
		return state, unanswered, fmt.Errorf("read update status: %w", err)
	}
	if status.State == "running" && now.Sub(status.StartedAt) >= staleUpdate {
		status.State = "failed"
	}
	state.Last = &status
	return state, unanswered, nil
}

// Start writes a request for version, replacing the file atomically so the
// updater never reads a partial version.
func (u Updater) Start(version string) error {
	if !releasePattern.MatchString(version) {
		return fmt.Errorf("invalid release %q", version)
	}
	temporary, err := os.CreateTemp(filepath.Dir(u.Request), ".update-request-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.WriteString(version + "\n"); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), u.Request)
}

func trimLine(data []byte) []byte {
	for index, character := range data {
		if character == '\n' || character == '\r' {
			return data[:index]
		}
	}
	return data
}
