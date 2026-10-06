// Package cores obtains official sing-box and Realm releases, verifies them
// against the SHA-256 digests GitHub publishes for release assets, and keeps
// the archives the panel serves to installers and Agents.
package cores

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KurisuT7/portolan/internal/model"
)

// Arches are the GOARCH values of the published Linux archives.
var Arches = []string{"amd64", "arm64"}

var (
	versionPattern = regexp.MustCompile(`^([0-9]+)\.([0-9]+)\.([0-9]+)(?:-[0-9A-Za-z.]+)?$`)
	stablePattern  = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	digestPattern  = regexp.MustCompile(`^sha256:([0-9a-f]{64})$`)
)

const (
	releaseCacheTTL = 10 * time.Minute
	listedReleases  = 8
	maxArchiveBytes = 512 << 20
)

type spec struct {
	repo    string
	binary  string
	minimum string
	asset   func(version, arch string) string
}

var specs = map[model.Core]spec{
	// Snell inbounds first shipped in sing-box 1.14.0.
	model.CoreSingBox: {repo: "SagerNet/sing-box", binary: "sing-box", minimum: "1.14.0",
		asset: func(version, arch string) string { return "sing-box-" + version + "-linux-" + arch + ".tar.gz" }},
	// Realm's plain gnu builds require a newer glibc than Debian 12 ships; the
	// static musl builds run on every supported host.
	model.CoreRealm: {repo: "zhboner/realm", binary: "realm", minimum: "2.9.4",
		asset: func(_, arch string) string {
			return map[string]string{"amd64": "realm-x86_64-unknown-linux-musl.tar.gz", "arm64": "realm-aarch64-unknown-linux-musl.tar.gz"}[arch]
		}},
}

// Binary is the executable name inside a core's release archive.
func Binary(core model.Core) string { return specs[core].binary }

// AssetName is the GitHub asset name of a core's Linux archive.
func AssetName(core model.Core, version, arch string) string { return specs[core].asset(version, arch) }

type Release struct {
	Version string `json:"version"`
}

type Client struct {
	API  string
	Root string
	HTTP *http.Client

	cacheMu sync.Mutex
	cache   map[model.Core]cachedReleases
	fetchMu sync.Mutex
}

type cachedReleases struct {
	releases []Release
	expires  time.Time
}

type githubRelease struct {
	Draft      bool `json:"draft"`
	Prerelease bool `json:"prerelease"`
	Assets     []struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
		URL    string `json:"browser_download_url"`
	} `json:"assets"`
}

func New(root string) *Client {
	return &Client{API: "https://api.github.com", Root: root, HTTP: &http.Client{Timeout: 5 * time.Minute}}
}

// Releases lists recent stable versions that Portolan supports, newest first.
// It reads tags because full release listings carry every asset (megabytes
// per page for sing-box); stable tags are plain X.Y.Z and Fetch confirms the
// release before anything is used.
func (c *Client) Releases(ctx context.Context, core model.Core) ([]Release, error) {
	c.cacheMu.Lock()
	cached, ok := c.cache[core]
	c.cacheMu.Unlock()
	if ok && time.Now().Before(cached.expires) {
		return cached.releases, nil
	}
	var tags []struct {
		Name string `json:"name"`
	}
	if err := c.getJSON(ctx, "/repos/"+specs[core].repo+"/tags?per_page=100", &tags); err != nil {
		return nil, err
	}
	releases := []Release{}
	for _, tag := range tags {
		version := strings.TrimPrefix(tag.Name, "v")
		if stablePattern.MatchString(version) && supported(core, version) {
			releases = append(releases, Release{Version: version})
		}
	}
	slices.SortFunc(releases, func(a, b Release) int { return compareVersions(b.Version, a.Version) })
	releases = releases[:min(len(releases), listedReleases)]
	c.cacheMu.Lock()
	if c.cache == nil {
		c.cache = map[model.Core]cachedReleases{}
	}
	c.cache[core] = cachedReleases{releases: releases, expires: time.Now().Add(releaseCacheTTL)}
	c.cacheMu.Unlock()
	return releases, nil
}

// Fetch stores the Linux archives of a stable release after checking each
// against its published digest, and returns those digests by GOARCH.
func (c *Client) Fetch(ctx context.Context, core model.Core, version string) (map[string]string, error) {
	if !supported(core, version) {
		return nil, fmt.Errorf("%s %s 不是受支持的版本（最低 %s）", core, version, specs[core].minimum)
	}
	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()
	var release githubRelease
	if err := c.getJSON(ctx, "/repos/"+specs[core].repo+"/releases/tags/v"+version, &release); err != nil {
		return nil, err
	}
	if release.Draft || release.Prerelease {
		return nil, fmt.Errorf("%s %s 不是正式版", core, version)
	}
	digests := map[string]string{}
	for _, arch := range Arches {
		name := specs[core].asset(version, arch)
		var url, expected string
		for _, asset := range release.Assets {
			if asset.Name == name {
				url = asset.URL
				if match := digestPattern.FindStringSubmatch(asset.Digest); match != nil {
					expected = match[1]
				}
			}
		}
		if url == "" || expected == "" {
			return nil, fmt.Errorf("%s %s 缺少 %s 或其官方 SHA-256 摘要", core, version, name)
		}
		if err := c.store(ctx, core, version, arch, url, expected); err != nil {
			return nil, err
		}
		digests[arch] = expected
	}
	return digests, nil
}

// Path is where the archive of a fetched release is kept.
func (c *Client) Path(core model.Core, version, arch string) string {
	return filepath.Join(c.Root, string(core), version, arch+".tar.gz")
}

// Prune removes every stored release of a core except keep.
func (c *Client) Prune(core model.Core, keep string) error {
	entries, err := os.ReadDir(filepath.Join(c.Root, string(core)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != keep && versionPattern.MatchString(entry.Name()) {
			failures = append(failures, os.RemoveAll(filepath.Join(c.Root, string(core), entry.Name())))
		}
	}
	return errors.Join(failures...)
}

func (c *Client) store(ctx context.Context, core model.Core, version, arch, url, expected string) error {
	destination := c.Path(core, version, arch)
	if existing, err := fileSHA256(destination); err == nil && existing == expected {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		return fmt.Errorf("下载 %s 失败：%w", path.Base(url), err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s 失败：GitHub 返回 %s", path.Base(url), response.Status)
	}
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(response.Body, maxArchiveBytes)); err != nil {
		return fmt.Errorf("下载 %s 失败：%w", path.Base(url), err)
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != expected {
		return fmt.Errorf("%s 的 SHA-256 与 GitHub 公布的摘要不一致", path.Base(url))
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := readBinary(temporary.Name(), specs[core].binary, func(r io.Reader) error {
		_, err := io.Copy(io.Discard, r)
		return err
	}); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), destination)
}

func (c *Client) getJSON(ctx context.Context, path string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.API+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "portolan-panel")
	response, err := c.HTTP.Do(request)
	if err != nil {
		return fmt.Errorf("无法连接 GitHub：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return errors.New("GitHub 上没有这个版本")
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub 返回 %s", response.Status)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(target)
}

// ExtractBinary writes the core executable from a release archive to
// destination with mode 0755.
func ExtractBinary(archive string, core model.Core, destination string) error {
	return readBinary(archive, specs[core].binary, func(r io.Reader) error {
		file, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(file, r); err != nil {
			file.Close()
			return err
		}
		return file.Close()
	})
}

func readBinary(archive, binary string, consume func(io.Reader) error) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	decompressed, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("archive is not gzip: %w", err)
	}
	reader := tar.NewReader(decompressed)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("archive does not contain %s", binary)
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if header.Typeflag == tar.TypeReg && path.Base(header.Name) == binary {
			return consume(io.LimitReader(reader, maxArchiveBytes))
		}
	}
}

func supported(core model.Core, version string) bool {
	return versionPattern.MatchString(version) && compareVersions(version, specs[core].minimum) >= 0
}

// compareVersions orders versions by their numeric major.minor.patch.
func compareVersions(a, b string) int {
	left, right := versionPattern.FindStringSubmatch(a), versionPattern.FindStringSubmatch(b)
	for index := 1; index <= 3; index++ {
		x, _ := strconv.Atoi(left[index])
		y, _ := strconv.Atoi(right[index])
		if x != y {
			return x - y
		}
	}
	return 0
}

// ValidVersion reports whether a version is a supported release of core.
func ValidVersion(core model.Core, version string) bool {
	return core.Valid() && supported(core, version)
}

func fileSHA256(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
