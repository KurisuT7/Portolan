package cores_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/KurisuT7/portolan/internal/cores"
	"github.com/KurisuT7/portolan/internal/cores/corestest"
	"github.com/KurisuT7/portolan/internal/model"
)

func TestReleasesListOnlySupportedStableVersions(t *testing.T) {
	github := corestest.GitHub(t,
		corestest.Release{Core: model.CoreSingBox, Version: "1.15.0-alpha.10", Prerelease: true},
		corestest.Release{Core: model.CoreSingBox, Version: "1.14.2"},
		corestest.Release{Core: model.CoreSingBox, Version: "1.13.21"},
		corestest.Release{Core: model.CoreRealm, Version: "2.9.6"},
		corestest.Release{Core: model.CoreRealm, Version: "2.9.2-2"},
	)
	client := corestest.Client(t, github)
	singBox, err := client.Releases(context.Background(), model.CoreSingBox)
	if err != nil {
		t.Fatal(err)
	}
	realm, err := client.Releases(context.Background(), model.CoreRealm)
	if err != nil {
		t.Fatal(err)
	}
	if len(singBox) != 1 || singBox[0].Version != "1.14.2" || len(realm) != 1 || realm[0].Version != "2.9.6" {
		t.Fatalf("releases: sing-box=%#v realm=%#v", singBox, realm)
	}
}

func TestFetchStoresVerifiedArchivesAndPrunesOthers(t *testing.T) {
	github := corestest.GitHub(t,
		corestest.Release{Core: model.CoreSingBox, Version: "1.14.2", Binary: []byte("old")},
		corestest.Release{Core: model.CoreSingBox, Version: "1.14.3", Binary: []byte("new")},
	)
	client := corestest.Client(t, github)
	if _, err := client.Fetch(context.Background(), model.CoreSingBox, "1.14.2"); err != nil {
		t.Fatal(err)
	}
	digests, err := client.Fetch(context.Background(), model.CoreSingBox, "1.14.3")
	if err != nil {
		t.Fatal(err)
	}
	want := corestest.SHA256(corestest.Archive(model.CoreSingBox, "1.14.3", []byte("new")))
	if !reflect.DeepEqual(digests, map[string]string{"amd64": want, "arm64": want}) {
		t.Fatalf("digests = %#v", digests)
	}
	archive := client.Path(model.CoreSingBox, "1.14.3", "arm64")
	binary := filepath.Join(t.TempDir(), "sing-box")
	if err := cores.ExtractBinary(archive, model.CoreSingBox, binary); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(binary); string(data) != "new" {
		t.Fatalf("extracted %q", data)
	}
	if err := client.Prune(model.CoreSingBox, "1.14.3"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(client.Path(model.CoreSingBox, "1.14.2", "amd64")); !os.IsNotExist(err) {
		t.Fatalf("superseded archive kept: %v", err)
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("target archive removed: %v", err)
	}
}

func TestFetchRejectsUnverifiableReleases(t *testing.T) {
	github := corestest.GitHub(t,
		corestest.Release{Core: model.CoreSingBox, Version: "1.14.4", Digest: "sha256:" + strings.Repeat("0", 64)},
		corestest.Release{Core: model.CoreSingBox, Version: "1.14.5", Digest: "none"},
		corestest.Release{Core: model.CoreSingBox, Version: "1.15.0", Prerelease: true},
	)
	client := corestest.Client(t, github)
	for version, want := range map[string]string{
		"1.14.4": "SHA-256",
		"1.14.5": "摘要",
		"1.15.0": "正式版",
		"1.13.0": "受支持",
		"1.14.9": "没有这个版本",
	} {
		_, err := client.Fetch(context.Background(), model.CoreSingBox, version)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %v, want %q", version, err, want)
		}
		if _, statErr := os.Stat(client.Path(model.CoreSingBox, version, "amd64")); !os.IsNotExist(statErr) {
			t.Errorf("%s: unverified archive stored", version)
		}
	}
}
