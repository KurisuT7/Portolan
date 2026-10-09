package api

import (
	"net/http"
	"time"

	"github.com/KurisuT7/Portolan/internal/buildinfo"
	"github.com/KurisuT7/Portolan/internal/panelupdate"
)

// panelUpdate reports the running and latest releases and the state of the
// updater, when this installation has one.
func (s *Server) panelUpdate(w http.ResponseWriter, r *http.Request) {
	response := map[string]any{"current": buildinfo.Version, "deployment": s.deployment, "updater": s.updater != nil}
	if latest, err := s.releases.Latest(r.Context()); err != nil {
		response["check_error"] = err.Error()
	} else {
		response["latest"] = latest
	}
	if s.updater != nil {
		state, unanswered, err := s.updater.Read(time.Now())
		if err != nil {
			s.internalError(w, err)
			return
		}
		response["pending"] = state.Pending
		response["unanswered"] = unanswered
		if state.Last != nil {
			response["last"] = state.Last
		}
	}
	writeJSON(w, http.StatusOK, response)
}

// startPanelUpdate asks the updater to install the latest release. Only the
// latest release can be requested, and only when it is newer than this one.
func (s *Server) startPanelUpdate(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Version string `json:"version"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.updater == nil {
		writeError(w, http.StatusConflict, "这个面板不是用安装脚本安装的，不能在线更新")
		return
	}
	latest, err := s.releases.Latest(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if request.Version != latest {
		writeError(w, http.StatusConflict, "只能更新到最新版本 "+latest)
		return
	}
	if !panelupdate.Newer(latest, buildinfo.Version) {
		writeError(w, http.StatusConflict, "面板已是最新版本")
		return
	}
	state, _, err := s.updater.Read(time.Now())
	if err != nil {
		s.internalError(w, err)
		return
	}
	if state.Busy() {
		writeError(w, http.StatusConflict, "面板正在更新")
		return
	}
	if err := s.updater.Start(latest); err != nil {
		s.internalError(w, err)
		return
	}
	s.logger.Info("panel update requested", "from", buildinfo.Version, "to", latest, "client", s.clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]any{"pending": latest})
}
