package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/KurisuT7/Portolan/internal/model"
)

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	servers, err := s.store.ListServers(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	nodes, err := s.store.ListNodes(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	forwards, err := s.store.ListForwards(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	online := 0
	for _, server := range servers {
		if observedAgentStatus(server, time.Now().UTC()) == "online" {
			online++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": len(servers), "online_servers": online, "nodes": len(nodes), "forwards": len(forwards)})
}

func (s *Server) listServers(w http.ResponseWriter, r *http.Request) {
	servers, err := s.store.ListServers(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	now := time.Now().UTC()
	for index := range servers {
		servers[index].AgentStatus = observedAgentStatus(servers[index], now)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": servers})
}

func observedAgentStatus(server model.Server, now time.Time) string {
	if server.AgentStatus != "online" {
		return server.AgentStatus
	}
	age := now.Sub(server.LastSeenAt)
	if server.LastSeenAt.IsZero() || age < 0 || age >= 2*time.Minute {
		return "offline"
	}
	return "online"
}

func (s *Server) createServer(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name    string `json:"name"`
		Address string `json:"address"`
		Region  string `json:"region"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	server := model.Server{Name: request.Name, Address: request.Address, Region: request.Region}
	created, token, err := s.store.CreateServer(r.Context(), server)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.enrollmentResponse(r, created, token))
}

func (s *Server) rotateServerEnrollment(w http.ResponseWriter, r *http.Request) {
	server, token, err := s.store.RotateEnrollmentToken(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.enrollmentResponse(r, server, token))
}

func (s *Server) enrollmentResponse(r *http.Request, server model.Server, token string) map[string]any {
	baseURL := s.externalURL(r)
	hint := "portolan-agent enroll --panel https://panel.example.com --token " + token
	if baseURL != "" && s.downloadsDir != "" {
		hint = "curl -fsSL '" + baseURL + "/api/v1/agent/bootstrap/" + token + "' | sudo sh"
	}
	return map[string]any{
		"server": server, "enrollment_token": token, "expires_in_seconds": 1200,
		"enrollment_hint": hint,
	}
}

func (s *Server) updateServer(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name    string `json:"name"`
		Address string `json:"address"`
		Region  string `json:"region"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	server, err := s.store.UpdateServer(r.Context(), r.PathValue("id"), request.Name, request.Address, request.Region)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, server)
}

func (s *Server) listConfigStatus(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.LatestSyncs(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	for index := range items {
		items[index].Result = publicConfigResult(items[index].State, items[index].Result)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func publicConfigResult(state, result string) string {
	if state != "failed" {
		return ""
	}
	message := "配置未能应用，请检查该服务器的 Agent 日志。"
	var reported struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(result), &reported) == nil {
		switch reported.Message {
		case "核心配置校验失败，未切换到新配置。",
			"新配置的端口没有监听成功，可能已被其他程序占用；Agent 已尝试恢复上一版配置。",
			"服务启动后没有保持运行；Agent 已尝试恢复上一版配置，请检查服务器日志。",
			"服务激活失败；Agent 已尝试恢复上一版配置，请检查服务器日志确认恢复结果。",
			"服务器缺少可用的 Realm 引擎，未切换到新配置。",
			"Agent 未回报应用结果，已重新下发当前配置。",
			"配置未能应用，请检查该服务器的 Agent 日志。":
			message = reported.Message
		}
	}
	encoded, _ := json.Marshal(map[string]string{"message": message})
	return string(encoded)
}

func (s *Server) syncServer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetServer(r.Context(), id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	if err := s.store.EnqueueSync(r.Context(), id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "queued"})
}

func (s *Server) deleteServer(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteServer(r.Context(), r.PathValue("id")); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
