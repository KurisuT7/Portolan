package api

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KurisuT7/Portolan/internal/model"
)

func (s *Server) agentBootstrap(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if !s.validEnrollmentDownload(w, r, token) {
		return
	}
	baseURL := s.externalURL(r)
	if baseURL == "" || s.downloadsDir == "" {
		writeError(w, http.StatusServiceUnavailable, "panel-hosted installation is not configured")
		return
	}
	agentAMD64, err := fileSHA256(filepath.Join(s.downloadsDir, "portolan-agent-linux-amd64"))
	if err != nil {
		s.internalError(w, err)
		return
	}
	agentARM64, err := fileSHA256(filepath.Join(s.downloadsDir, "portolan-agent-linux-arm64"))
	if err != nil {
		s.internalError(w, err)
		return
	}
	targets, err := s.store.CoreTargets(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	singBox, singBoxReady := targets[model.CoreSingBox]
	realm, realmReady := targets[model.CoreRealm]
	if !singBoxReady || !realmReady {
		writeError(w, http.StatusServiceUnavailable, "面板尚未选择 sing-box 和 Realm 版本")
		return
	}
	downloadBase := baseURL + "/api/v1/agent/downloads/" + token
	script := fmt.Sprintf(`#!/bin/sh
set -eu
case "$(uname -m)" in
  x86_64|amd64) arch=amd64; agent_sha=%[1]s; sing_box_sha=%[3]s; realm_sha=%[5]s ;;
  aarch64|arm64) arch=arm64; agent_sha=%[2]s; sing_box_sha=%[4]s; realm_sha=%[6]s ;;
  *) printf 'portolan bootstrap: unsupported architecture %%s\n' "$(uname -m)" >&2; exit 1 ;;
esac
tmp=$(mktemp /tmp/portolan-bootstrap.XXXXXX)
trap 'rm -f -- "$tmp"' EXIT HUP INT TERM
curl --fail --location --silent --show-error --retry 3 --proto '=https' --tlsv1.2 -o "$tmp" '%[7]s/install-agent.sh'
sh "$tmp" --panel '%[8]s' --token '%[9]s' \
  --agent-url "%[7]s/portolan-agent-linux-$arch" --agent-sha256 "$agent_sha" \
  --sing-box-url "%[7]s/cores/sing-box/$arch" --sing-box-sha256 "$sing_box_sha" \
  --realm-url "%[7]s/cores/realm/$arch" --realm-sha256 "$realm_sha"
`, agentAMD64, agentARM64, singBox.SHA256["amd64"], singBox.SHA256["arm64"], realm.SHA256["amd64"], realm.SHA256["arm64"],
		downloadBase, baseURL, token)
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(script))
}

func (s *Server) agentDownload(w http.ResponseWriter, r *http.Request) {
	if !s.validEnrollmentDownload(w, r, r.PathValue("token")) {
		return
	}
	name := r.PathValue("name")
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `\\/`) {
		writeError(w, http.StatusBadRequest, "invalid download name")
		return
	}
	path := filepath.Join(s.downloadsDir, name)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "download is unavailable")
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(archiveWriteTimeout))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, path)
}

func (s *Server) validEnrollmentDownload(w http.ResponseWriter, r *http.Request, token string) bool {
	valid, err := s.store.EnrollmentTokenValid(r.Context(), token)
	if err != nil {
		s.internalError(w, err)
		return false
	}
	if !valid {
		writeError(w, http.StatusUnauthorized, "invalid or expired enrollment token")
		return false
	}
	return true
}

func (s *Server) externalURL(r *http.Request) string {
	if s.publicURL != "" {
		return s.publicURL
	}
	proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if proto == "" {
		proto = "https"
	}
	if r.Host == "" {
		return ""
	}
	return proto + "://" + r.Host
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
