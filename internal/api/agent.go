package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/KurisuT7/portolan/internal/model"
	"github.com/KurisuT7/portolan/internal/store"
)

func (s *Server) enrollAgent(w http.ResponseWriter, r *http.Request) {
	var request struct {
		EnrollmentToken string   `json:"enrollment_token"`
		PublicAddresses []string `json:"public_addresses"`
		EgressFamilies  []string `json:"egress_families"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ipv4Address, ipv6Address := agentPublicAddresses(request.PublicAddresses)
	egressIPv4, egressIPv6 := agentEgressFamilies(request.EgressFamilies)
	observedAddress := preferredAgentAddress(s.clientIP(r), request.PublicAddresses)
	serverID, token, err := s.store.Enroll(r.Context(), request.EnrollmentToken, observedAddress)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid or expired enrollment token")
		return
	}
	if request.PublicAddresses != nil || request.EgressFamilies != nil {
		if _, updateErr := s.store.UpdateAgentNetwork(r.Context(), serverID, store.AgentNetworkUpdate{
			AddressesKnown: request.PublicAddresses != nil,
			IPv4Address:    ipv4Address,
			IPv6Address:    ipv6Address,
			EgressKnown:    request.EgressFamilies != nil,
			EgressIPv4:     egressIPv4,
			EgressIPv6:     egressIPv6,
		}); updateErr != nil {
			s.logger.Warn("automatic Agent address-family update failed", "server_id", serverID, "error", updateErr)
		}
	}
	if s.regionLookup != nil {
		region, lookupErr := s.regionLookup(observedAddress)
		if lookupErr != nil {
			s.logger.Warn("automatic server location lookup failed", "server_id", serverID, "error", lookupErr)
		} else if region != "" {
			if updateErr := s.store.SetServerRegionIfBlank(r.Context(), serverID, region); updateErr != nil {
				s.logger.Warn("automatic server location update failed", "server_id", serverID, "error", updateErr)
			}
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"server_id": serverID, "agent_token": token})
}

func preferredAgentAddress(observed string, reported []string) string {
	ipv4Address, ipv6Address := agentPublicAddresses(reported)
	if ipv4Address != "" {
		return ipv4Address
	}
	if ipv6Address != "" {
		return ipv6Address
	}
	if model.IsPublicRoutableIP(observed) {
		return net.ParseIP(model.NormalizeHost(observed)).String()
	}
	return observed
}

func agentPublicAddresses(reported []string) (ipv4Address, ipv6Address string) {
	for _, candidate := range reported {
		candidate = model.NormalizeHost(candidate)
		ip := net.ParseIP(candidate)
		if ip == nil || !model.IsPublicRoutableIP(candidate) {
			continue
		}
		if ip.To4() != nil && ipv4Address == "" {
			ipv4Address = ip.String()
		}
		if ip.To4() == nil && ipv6Address == "" {
			ipv6Address = ip.String()
		}
	}
	return ipv4Address, ipv6Address
}

func agentEgressFamilies(reported []string) (ipv4, ipv6 bool) {
	for _, candidate := range reported {
		switch strings.ToLower(strings.TrimSpace(candidate)) {
		case "ipv4":
			ipv4 = true
		case "ipv6":
			ipv6 = true
		}
	}
	return ipv4, ipv6
}

func (s *Server) agentForwards(w http.ResponseWriter, r *http.Request) {
	serverID := r.Context().Value(agentContextKey{}).(string)
	items, err := s.store.ListForwardsForServer(r.Context(), serverID)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) saveAgentDiscoveredNodes(w http.ResponseWriter, r *http.Request) {
	serverID := r.Context().Value(agentContextKey{}).(string)
	var request struct {
		Items []model.Node `json:"items"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(request.Items) > 100 {
		writeError(w, http.StatusBadRequest, "too many discovered nodes")
		return
	}
	created, updated, err := s.store.UpsertDiscoveredNodes(r.Context(), serverID, request.Items)
	if err != nil {
		s.logger.Warn("agent discovery report rejected", "server_id", serverID, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": created, "updated": updated})
}

func (s *Server) saveAgentForwardProbes(w http.ResponseWriter, r *http.Request) {
	serverID := r.Context().Value(agentContextKey{}).(string)
	var request struct {
		Items []model.ForwardProbe `json:"items"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(request.Items) > 200 {
		writeError(w, http.StatusBadRequest, "too many probe results")
		return
	}
	if err := s.store.SaveForwardProbes(r.Context(), serverID, request.Items); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) saveAgentStatus(w http.ResponseWriter, r *http.Request) {
	serverID := r.Context().Value(agentContextKey{}).(string)
	var status model.RuntimeStatus
	if err := decodeJSON(w, r, &status); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := status.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.SaveRuntimeStatus(r.Context(), serverID, status); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) nextAgentJob(w http.ResponseWriter, r *http.Request) {
	serverID := r.Context().Value(agentContextKey{}).(string)
	if err := s.store.TouchAgent(r.Context(), serverID); err != nil {
		s.internalError(w, err)
		return
	}
	deadline := time.Now().Add(agentJobWait(r.URL.Query().Get("wait")))
	for {
		changed, unwatch := s.store.WatchJobs(serverID)
		job, err := s.store.NextJob(r.Context(), serverID)
		if err != nil {
			unwatch()
			s.internalError(w, err)
			return
		}
		if job != nil {
			unwatch()
			writeJSON(w, http.StatusOK, job)
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			unwatch()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		timer := time.NewTimer(remaining)
		select {
		case <-r.Context().Done():
			timer.Stop()
			unwatch()
			return
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
		unwatch()
	}
}

func agentJobWait(raw string) time.Duration {
	waitSeconds, _ := strconv.Atoi(raw)
	if waitSeconds < 0 {
		waitSeconds = 0
	}
	if waitSeconds > int(maxAgentJobWait/time.Second) {
		return maxAgentJobWait
	}
	return time.Duration(waitSeconds) * time.Second
}

func (s *Server) completeAgentJob(w http.ResponseWriter, r *http.Request) {
	serverID := r.Context().Value(agentContextKey{}).(string)
	var request struct {
		Success bool   `json:"success"`
		Result  string `json:"result"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.CompleteJob(r.Context(), serverID, r.PathValue("id"), request.Success, request.Result); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
