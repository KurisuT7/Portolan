package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/KurisuT7/Portolan/internal/agentproto"
	"github.com/KurisuT7/Portolan/internal/buildinfo"
	"github.com/KurisuT7/Portolan/internal/cores"
	"github.com/KurisuT7/Portolan/internal/model"
)

// agentSelfUpdateSince is the first Agent release that installs updates sent
// by the panel; older Agents are updated by running the installer again.
const agentSelfUpdateSince = "v0.2.0"

var releasePattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)`)

// olderRelease reports whether release a precedes release b. Versions that are
// not releases, such as "dev", compare as neither older nor newer.
func olderRelease(a, b string) bool {
	left, right := releasePattern.FindStringSubmatch(a), releasePattern.FindStringSubmatch(b)
	if left == nil || right == nil {
		return false
	}
	for index := 1; index <= 3; index++ {
		x, _ := strconv.Atoi(left[index])
		y, _ := strconv.Atoi(right[index])
		if x != y {
			return x < y
		}
	}
	return false
}

// agentUpdatePayload describes the Agent binaries this panel serves.
func (s *Server) agentUpdatePayload() (agentproto.AgentUpdatePayload, error) {
	version := buildinfo.AgentVersion
	if s.downloadsDir == "" || !releasePattern.MatchString(version) {
		return agentproto.AgentUpdatePayload{}, errors.New("这个面板没有可分发的 Agent 发布版本")
	}
	digests := map[string]string{}
	for _, arch := range cores.Arches {
		digest, err := fileSHA256(filepath.Join(s.downloadsDir, "portolan-agent-linux-"+arch))
		if err != nil {
			return agentproto.AgentUpdatePayload{}, err
		}
		digests[arch] = digest
	}
	return agentproto.AgentUpdatePayload{Version: version, SHA256: digests}, nil
}

// agentUpdateBlocker explains why a server's Agent cannot take the update, or
// returns "" when it can.
func agentUpdateBlocker(server model.Server, target string) string {
	if server.Runtime == nil {
		return "这台服务器的 Agent 还没有上报状态"
	}
	if olderRelease(server.Runtime.AgentVersion, agentSelfUpdateSince) {
		return "这台服务器的 Agent 版本较旧，需要先重装一次 Agent"
	}
	if !releasePattern.MatchString(server.Runtime.AgentVersion) {
		return "这台服务器运行的是开发版 Agent，不能在线更新"
	}
	if !olderRelease(server.Runtime.AgentVersion, target) {
		return "Agent 已是面板提供的版本"
	}
	return ""
}

func (s *Server) updateServerAgent(w http.ResponseWriter, r *http.Request) {
	server, err := s.store.GetServer(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	payload, err := s.agentUpdatePayload()
	if err != nil {
		s.logger.Warn("Agent update is unavailable", "error", err)
		writeError(w, http.StatusServiceUnavailable, "这个面板没有可分发的 Agent 发布版本")
		return
	}
	if blocker := agentUpdateBlocker(server, payload.Version); blocker != "" {
		writeError(w, http.StatusConflict, blocker)
		return
	}
	job, err := s.store.EnqueueAgentUpdate(r.Context(), server.ID, payload)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

// rolloutAgent queues the update for every Agent that can install it.
func (s *Server) rolloutAgent(w http.ResponseWriter, r *http.Request) {
	payload, err := s.agentUpdatePayload()
	if err != nil {
		s.logger.Warn("Agent update is unavailable", "error", err)
		writeError(w, http.StatusServiceUnavailable, "这个面板没有可分发的 Agent 发布版本")
		return
	}
	servers, err := s.store.ListServers(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	queued := 0
	for _, server := range servers {
		if agentUpdateBlocker(server, payload.Version) != "" {
			continue
		}
		if _, err := s.store.EnqueueAgentUpdate(r.Context(), server.ID, payload); err != nil {
			s.writeStoreError(w, err)
			return
		}
		queued++
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": queued})
}

// agentBinaryDownload serves the Agent binary to an authenticated Agent.
func (s *Server) agentBinaryDownload(w http.ResponseWriter, r *http.Request) {
	arch := r.PathValue("arch")
	if s.downloadsDir == "" || !slices.Contains(cores.Arches, arch) {
		writeError(w, http.StatusNotFound, "Agent binary is unavailable")
		return
	}
	path := filepath.Join(s.downloadsDir, "portolan-agent-linux-"+arch)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "Agent binary is unavailable")
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(archiveWriteTimeout))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, path)
}
