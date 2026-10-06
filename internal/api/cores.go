package api

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/KurisuT7/Portolan/internal/cores"
	"github.com/KurisuT7/Portolan/internal/model"
)

// archiveWriteTimeout lets slow servers finish downloading a core archive.
const archiveWriteTimeout = 15 * time.Minute

func (s *Server) listCores(w http.ResponseWriter, r *http.Request) {
	targets, err := s.store.CoreTargets(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	jobs, err := s.store.LatestCoreUpdates(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	for index := range jobs {
		jobs[index].Result = publicCoreResult(jobs[index].State, jobs[index].Result)
	}
	items := []model.CoreTarget{}
	for _, core := range []model.Core{model.CoreSingBox, model.CoreRealm} {
		if target, ok := targets[core]; ok {
			items = append(items, target)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": items, "jobs": jobs})
}

func (s *Server) listCoreReleases(w http.ResponseWriter, r *http.Request) {
	core, ok := s.pathCore(w, r)
	if !ok {
		return
	}
	releases, err := s.cores.Releases(r.Context(), core)
	if err != nil {
		writeError(w, http.StatusBadGateway, "无法从 GitHub 读取版本列表："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": releases})
}

// setCoreTarget downloads and verifies a release, then makes it the version
// new installs receive and updates install.
func (s *Server) setCoreTarget(w http.ResponseWriter, r *http.Request) {
	core, ok := s.pathCore(w, r)
	if !ok {
		return
	}
	var request struct {
		Version string `json:"version"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !cores.ValidVersion(core, request.Version) {
		writeError(w, http.StatusBadRequest, "版本号无效或低于 Portolan 支持的最低版本")
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(archiveWriteTimeout))
	digests, err := s.cores.Fetch(r.Context(), core, request.Version)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	target := model.CoreTarget{Core: core, Version: request.Version, SHA256: digests}
	if err := s.store.SetCoreTarget(r.Context(), target); err != nil {
		s.internalError(w, err)
		return
	}
	if err := s.cores.Prune(core, request.Version); err != nil {
		s.logger.Warn("removing superseded core archives failed", "core", core, "error", err)
	}
	writeJSON(w, http.StatusOK, target)
}

// rolloutCore queues the target release for every server whose Agent reports
// its runtime and runs another version. Older Agents cannot update in place.
func (s *Server) rolloutCore(w http.ResponseWriter, r *http.Request) {
	core, ok := s.pathCore(w, r)
	if !ok {
		return
	}
	targets, err := s.store.CoreTargets(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	target, ok := targets[core]
	if !ok {
		writeError(w, http.StatusConflict, "尚未选择目标版本")
		return
	}
	servers, err := s.store.ListServers(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	queued := 0
	for _, server := range servers {
		if server.Runtime == nil || installedVersion(server, core) == target.Version {
			continue
		}
		if _, err := s.store.EnqueueCoreUpdate(r.Context(), server.ID, core); err != nil {
			s.writeStoreError(w, err)
			return
		}
		queued++
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": queued})
}

func (s *Server) updateServerCore(w http.ResponseWriter, r *http.Request) {
	core, ok := s.pathCore(w, r)
	if !ok {
		return
	}
	server, err := s.store.GetServer(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if server.Runtime == nil {
		writeError(w, http.StatusConflict, "这台服务器的 Agent 版本较旧，重装 Agent 后才能在线更新核心")
		return
	}
	job, err := s.store.EnqueueCoreUpdate(r.Context(), server.ID, core)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

// agentCoreDownload serves a stored release archive to an authenticated Agent.
func (s *Server) agentCoreDownload(w http.ResponseWriter, r *http.Request) {
	core, ok := s.pathCore(w, r)
	if !ok {
		return
	}
	version, arch := r.PathValue("version"), r.PathValue("arch")
	if !cores.ValidVersion(core, version) || !slices.Contains(cores.Arches, arch) {
		writeError(w, http.StatusBadRequest, "invalid core archive")
		return
	}
	s.serveArchive(w, r, s.cores.Path(core, version, arch))
}

// installerCoreDownload serves the target archive to an installer that holds
// a valid enrollment token.
func (s *Server) installerCoreDownload(w http.ResponseWriter, r *http.Request) {
	if !s.validEnrollmentDownload(w, r, r.PathValue("token")) {
		return
	}
	core, ok := s.pathCore(w, r)
	if !ok {
		return
	}
	arch := r.PathValue("arch")
	targets, err := s.store.CoreTargets(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	target, ok := targets[core]
	if !ok || !slices.Contains(cores.Arches, arch) {
		writeError(w, http.StatusNotFound, "core archive is unavailable")
		return
	}
	s.serveArchive(w, r, s.cores.Path(core, target.Version, arch))
}

func (s *Server) serveArchive(w http.ResponseWriter, r *http.Request, path string) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "core archive is unavailable")
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(archiveWriteTimeout))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/gzip")
	http.ServeFile(w, r, path)
}

func (s *Server) pathCore(w http.ResponseWriter, r *http.Request) (model.Core, bool) {
	core := model.Core(r.PathValue("core"))
	if !core.Valid() {
		writeError(w, http.StatusNotFound, "unknown core")
		return "", false
	}
	return core, true
}

func installedVersion(server model.Server, core model.Core) string {
	if server.Runtime == nil {
		return ""
	}
	if core == model.CoreRealm {
		return server.Runtime.RealmVersion
	}
	return server.Runtime.SingBoxVersion
}

func publicCoreResult(state, result string) string {
	if state != "failed" {
		return ""
	}
	message := "核心更新失败，请检查该服务器的 Agent 日志。"
	var reported struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(result), &reported) == nil {
		switch reported.Message {
		case "核心更新请求无效。",
			"核心文件下载或校验失败，未替换。",
			"新版本 sing-box 不接受当前配置，未替换。",
			"新核心没有正常运行，已换回原版本。",
			"新核心没有正常运行，换回原版本时也出错，请立即检查服务器。":
			message = reported.Message
		}
	}
	encoded, _ := json.Marshal(map[string]string{"message": message})
	return string(encoded)
}
