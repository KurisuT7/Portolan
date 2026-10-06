// Package corestest builds core release archives and serves them through a
// fake GitHub releases API for tests.
package corestest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KurisuT7/Portolan/internal/cores"
	"github.com/KurisuT7/Portolan/internal/model"
)

type Release struct {
	Core       model.Core
	Version    string
	Prerelease bool
	// Binary is the executable content placed in every archive.
	Binary []byte
	// Digest replaces the published digest of every asset when set.
	Digest string
}

// Archive builds a release archive that contains the core executable.
func Archive(core model.Core, version string, binary []byte) []byte {
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	name := cores.Binary(core)
	if core == model.CoreSingBox {
		name = "sing-box-" + version + "-linux-amd64/" + name
	}
	_ = archive.WriteHeader(&tar.Header{Name: "README.md", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	_, _ = archive.Write([]byte("ok"))
	_ = archive.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg})
	_, _ = archive.Write(binary)
	_ = archive.Close()
	_ = compressed.Close()
	return buffer.Bytes()
}

// SHA256 returns the hex digest of data.
func SHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// GitHub serves the tag list, the release lookup by tag and the assets of the
// given releases, listing tags in the order given.
func GitHub(t testing.TB, releases ...Release) *httptest.Server {
	t.Helper()
	assets := map[string][]byte{}
	tagNames := map[model.Core][]map[string]string{}
	tags := map[string]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if data, ok := assets[r.URL.Path]; ok {
			_, _ = w.Write(data)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		switch {
		case len(parts) == 4 && parts[0] == "repos" && parts[3] == "tags":
			_ = json.NewEncoder(w).Encode(tagNames[model.Core(parts[2])])
		case len(parts) == 6 && parts[0] == "repos" && parts[4] == "tags" && tags[parts[2]+"/"+parts[5]] != nil:
			_ = json.NewEncoder(w).Encode(tags[parts[2]+"/"+parts[5]])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	for _, release := range releases {
		var items []map[string]any
		for _, arch := range cores.Arches {
			name := cores.AssetName(release.Core, release.Version, arch)
			data := Archive(release.Core, release.Version, release.Binary)
			path := "/assets/" + string(release.Core) + "/" + release.Version + "/" + name
			assets[path] = data
			digest := "sha256:" + SHA256(data)
			if release.Digest != "" {
				digest = release.Digest
			}
			items = append(items, map[string]any{"name": name, "digest": digest, "browser_download_url": server.URL + path})
		}
		described := map[string]any{"tag_name": "v" + release.Version, "draft": false, "prerelease": release.Prerelease, "assets": items}
		tagNames[release.Core] = append(tagNames[release.Core], map[string]string{"name": "v" + release.Version})
		tags[string(release.Core)+"/v"+release.Version] = described
	}
	return server
}

// Client returns a core release client backed by a fake GitHub server.
func Client(t testing.TB, github *httptest.Server) *cores.Client {
	t.Helper()
	return &cores.Client{API: github.URL, Root: t.TempDir(), HTTP: github.Client()}
}
