package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/KurisuT7/Portolan/internal/configgen"
	"github.com/KurisuT7/Portolan/internal/model"
)

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.store.ListNodes(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": nodes})
}

func (s *Server) createNode(w http.ResponseWriter, r *http.Request) {
	var request createNodeRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	server, err := s.store.GetServer(r.Context(), request.ServerID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	node := model.Node{ServerID: request.ServerID, Name: request.Name, Protocol: request.Protocol, ListenPort: request.ListenPort, Enabled: true}
	profile := ""
	switch request.Protocol {
	case model.ProtocolReality:
		// Xray-based clients accept REALITY only over raw TCP, XHTTP and gRPC,
		// and Vision only over raw TCP.
		if request.Reality.Transport == "" {
			request.Reality.Transport = "tcp"
		}
		if request.Reality.Transport != "tcp" && request.Reality.Transport != "grpc" {
			writeError(w, http.StatusBadRequest, "Reality 只支持 TCP 或 gRPC 传输")
			return
		}
		if request.Reality.Flow != "" && request.Reality.Transport != "tcp" {
			writeError(w, http.StatusBadRequest, "Vision 流控只能用于 TCP 传输")
			return
		}
		uuid, err := configgen.NewUUID()
		if err != nil {
			s.internalError(w, err)
			return
		}
		privateKey, publicKey, err := configgen.RealityKeyPair()
		if err != nil {
			s.internalError(w, err)
			return
		}
		shortID, err := configgen.RandomShortID()
		if err != nil {
			s.internalError(w, err)
			return
		}
		if request.Reality.HandshakePort == 0 {
			request.Reality.HandshakePort = 443
		}
		if request.Reality.ServerName == "" {
			request.Reality.ServerName = request.Reality.HandshakeServer
		}
		if request.Reality.Fingerprint == "" {
			request.Reality.Fingerprint = "chrome"
		}
		node.Reality = &model.RealitySpec{UUID: uuid, Flow: request.Reality.Flow, HandshakeServer: request.Reality.HandshakeServer,
			HandshakePort: request.Reality.HandshakePort, ServerName: request.Reality.ServerName, PrivateKey: privateKey, PublicKey: publicKey,
			ShortIDs: []string{shortID}, Fingerprint: request.Reality.Fingerprint, Transport: request.Reality.Transport,
			TransportSettings: request.Reality.TransportSettings, MaxTimeDifference: request.Reality.MaxTimeDifference}
		profile = "REALITY · " + valueOr(request.Reality.Flow, "standard")
	case model.ProtocolShadowsocks:
		password, err := configgen.ShadowsocksPassword(request.Shadowsocks.Method)
		if err != nil {
			s.internalError(w, err)
			return
		}
		node.SS = &model.SSSpec{Method: request.Shadowsocks.Method, Password: password, AllowInsecure: request.Shadowsocks.AllowInsecure}
		profile = request.Shadowsocks.Method
	case model.ProtocolSnell:
		psk, err := configgen.RandomURLSafe(32)
		if err != nil {
			s.internalError(w, err)
			return
		}
		node.Snell = &model.SnellSpec{Version: request.Snell.Version, PSK: psk, ObfsMode: request.Snell.ObfsMode,
			Mode: request.Snell.Mode, AllowInsecure: request.Snell.AllowInsecure}
		profile = "Snell v" + strconv.Itoa(request.Snell.Version)
	default:
		writeError(w, http.StatusBadRequest, "unsupported protocol")
		return
	}
	created, err := s.store.CreateNode(r.Context(), node, profile)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if strings.TrimSpace(server.Address) == "" {
		writeJSON(w, http.StatusCreated, map[string]any{"id": created.ID, "client": configgen.ClientExport{}, "export_pending": true})
		return
	}
	client, err := configgen.ExportClient(created, configgen.PublishedEndpoint{Address: server.Address, Port: created.ListenPort, Name: created.Name})
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": created.ID, "client": client})
}

func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteNode(r.Context(), r.PathValue("id")); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) exportNode(w http.ResponseWriter, r *http.Request) {
	node, err := s.store.GetNode(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	server, err := s.store.GetServer(r.Context(), node.ServerID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	address := valueOr(r.URL.Query().Get("address"), server.Address)
	if strings.TrimSpace(address) == "" {
		writeError(w, http.StatusConflict, "server address is still waiting for Agent auto-detection")
		return
	}
	port := node.ListenPort
	if rawPort := r.URL.Query().Get("port"); rawPort != "" {
		parsed, err := strconv.ParseUint(rawPort, 10, 16)
		if err != nil || parsed == 0 {
			writeError(w, http.StatusBadRequest, "invalid published port")
			return
		}
		port = uint16(parsed)
	}
	name := valueOr(r.URL.Query().Get("name"), node.Name)
	client, err := configgen.ExportClient(node, configgen.PublishedEndpoint{Address: address, Port: port, Name: name})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, client)
}

type createNodeRequest struct {
	ServerID   string         `json:"server_id"`
	Name       string         `json:"name"`
	Protocol   model.Protocol `json:"protocol"`
	ListenPort uint16         `json:"listen_port"`
	Reality    struct {
		Flow              string            `json:"flow"`
		HandshakeServer   string            `json:"handshake_server"`
		HandshakePort     uint16            `json:"handshake_port"`
		ServerName        string            `json:"server_name"`
		Fingerprint       string            `json:"fingerprint"`
		Transport         string            `json:"transport"`
		TransportSettings map[string]string `json:"transport_settings"`
		MaxTimeDifference string            `json:"max_time_difference"`
	} `json:"reality"`
	Shadowsocks struct {
		Method        string `json:"method"`
		AllowInsecure bool   `json:"allow_insecure"`
	} `json:"shadowsocks"`
	Snell struct {
		Version       int    `json:"version"`
		ObfsMode      string `json:"obfs_mode"`
		Mode          string `json:"mode"`
		AllowInsecure bool   `json:"allow_insecure"`
	} `json:"snell"`
}
