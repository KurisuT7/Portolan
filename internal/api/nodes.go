package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/KurisuT7/Portolan/internal/configgen"
	"github.com/KurisuT7/Portolan/internal/model"
	"github.com/KurisuT7/Portolan/internal/store"
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
	profile, err := buildNode(server, &node, request.nodeSettings, nil)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	created, err := s.store.CreateNode(r.Context(), node, profile)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	result, err := savedNode(server, created)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

// getNode returns the settings of a node without its credentials.
func (s *Server) getNode(w http.ResponseWriter, r *http.Request) {
	node, err := s.store.GetNode(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, viewNode(node))
}

// PUT replaces the settings of a managed node. Its credentials are kept unless
// rotate_credentials is set, so client links stay valid while the settings in
// them do not change. The Agent applies the new desired state once committed.
func (s *Server) updateNode(w http.ResponseWriter, r *http.Request) {
	var request updateNodeRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	previous, err := s.store.GetNode(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !previous.Managed {
		s.writeStoreError(w, store.ErrReadOnly)
		return
	}
	server, err := s.store.GetServer(r.Context(), previous.ServerID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	node := previous
	node.Name, node.ListenPort = request.Name, request.ListenPort
	kept := &previous
	if request.RotateCredentials {
		kept = nil
	}
	profile, err := buildNode(server, &node, request.nodeSettings, kept)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	updated, updatedAt, err := s.store.UpdateNode(r.Context(), node, profile)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	result, err := savedNode(server, updated)
	if err != nil {
		s.internalError(w, err)
		return
	}
	if !updatedAt.IsZero() {
		result["updated_at"] = updatedAt
	}
	writeJSON(w, http.StatusOK, result)
}

// buildNode fills the protocol settings of node. Credentials of previous are
// kept while they still fit; a nil previous generates new ones.
func buildNode(server model.Server, node *model.Node, settings nodeSettings, previous *model.Node) (string, error) {
	node.Reality, node.SS, node.Snell, node.ShadowTLS = nil, nil, nil, nil
	if settings.ShadowTLS != nil {
		// Older Agents ignore the wrapper and would serve the bare protocol.
		if server.Runtime != nil && olderRelease(server.Runtime.AgentVersion, shadowTLSAgentSince) {
			return "", fmt.Errorf("%w: %s 的 Agent 是 %s，更新到 %s 或更高版本后才能使用 ShadowTLS", store.ErrConflict,
				server.Name, server.Runtime.AgentVersion, shadowTLSAgentSince)
		}
		spec := model.ShadowTLSSpec{HandshakeServer: strings.TrimSpace(settings.ShadowTLS.HandshakeServer), HandshakePort: settings.ShadowTLS.HandshakePort}
		if spec.HandshakePort == 0 {
			spec.HandshakePort = 443
		}
		if previous != nil && previous.ShadowTLS != nil {
			spec.Password = previous.ShadowTLS.Password
		} else {
			password, err := configgen.RandomURLSafe(16)
			if err != nil {
				return "", err
			}
			spec.Password = password
		}
		node.ShadowTLS = &spec
	}
	profile := ""
	switch node.Protocol {
	case model.ProtocolReality:
		reality := settings.Reality
		// Xray-based clients accept REALITY only over raw TCP, XHTTP and gRPC,
		// and Vision only over raw TCP.
		if reality.Transport == "" {
			reality.Transport = "tcp"
		}
		if reality.Transport != "tcp" && reality.Transport != "grpc" {
			return "", fmt.Errorf("%w: Reality 只支持 TCP 或 gRPC 传输", store.ErrInvalidInput)
		}
		if reality.Flow != "" && reality.Transport != "tcp" {
			return "", fmt.Errorf("%w: Vision 流控只能用于 TCP 传输", store.ErrInvalidInput)
		}
		spec := model.RealitySpec{Flow: reality.Flow, HandshakeServer: reality.HandshakeServer, HandshakePort: reality.HandshakePort,
			ServerName: valueOr(reality.ServerName, reality.HandshakeServer), Fingerprint: valueOr(reality.Fingerprint, "chrome"),
			Transport: reality.Transport, TransportSettings: reality.TransportSettings, MaxTimeDifference: reality.MaxTimeDifference}
		if spec.HandshakePort == 0 {
			spec.HandshakePort = 443
		}
		if previous != nil && previous.Reality != nil {
			spec.UUID, spec.PrivateKey, spec.PublicKey = previous.Reality.UUID, previous.Reality.PrivateKey, previous.Reality.PublicKey
			spec.ShortIDs = previous.Reality.ShortIDs
		} else {
			uuid, err := configgen.NewUUID()
			if err != nil {
				return "", err
			}
			privateKey, publicKey, err := configgen.RealityKeyPair()
			if err != nil {
				return "", err
			}
			shortID, err := configgen.RandomShortID()
			if err != nil {
				return "", err
			}
			spec.UUID, spec.PrivateKey, spec.PublicKey, spec.ShortIDs = uuid, privateKey, publicKey, []string{shortID}
		}
		node.Reality = &spec
		profile = "REALITY · " + valueOr(spec.Flow, "standard")
	case model.ProtocolShadowsocks:
		spec := model.SSSpec{Method: settings.Shadowsocks.Method, AllowInsecure: settings.Shadowsocks.AllowInsecure}
		// Each method has its own key length, and clients change the method anyway.
		if previous != nil && previous.SS != nil && previous.SS.Method == spec.Method {
			spec.Password = previous.SS.Password
		} else {
			password, err := configgen.ShadowsocksPassword(spec.Method)
			if err != nil {
				return "", err
			}
			spec.Password = password
		}
		node.SS = &spec
		profile = spec.Method
	case model.ProtocolSnell:
		spec := model.SnellSpec{Version: settings.Snell.Version, ObfsMode: settings.Snell.ObfsMode, Mode: settings.Snell.Mode,
			AllowInsecure: settings.Snell.AllowInsecure}
		if previous != nil && previous.Snell != nil {
			spec.PSK = previous.Snell.PSK
		} else {
			psk, err := configgen.RandomURLSafe(32)
			if err != nil {
				return "", err
			}
			spec.PSK = psk
		}
		node.Snell = &spec
		profile = "Snell v" + strconv.Itoa(spec.Version)
	default:
		return "", fmt.Errorf("%w: unsupported protocol", store.ErrInvalidInput)
	}
	if node.ShadowTLS != nil {
		profile += " · ShadowTLS"
	}
	return profile, nil
}

// savedNode describes a created or updated node with its client link, which
// waits while the server address is still being detected.
func savedNode(server model.Server, node model.Node) (map[string]any, error) {
	if strings.TrimSpace(server.Address) == "" {
		return map[string]any{"id": node.ID, "client": configgen.ClientExport{}, "export_pending": true}, nil
	}
	// Ports of a NAT server are allocated and checked within its mappings.
	port, _ := server.PublicPort(server.Address, node.ListenPort)
	client, err := configgen.ExportClient(node, configgen.PublishedEndpoint{Address: server.Address, Port: port, Name: node.Name})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": node.ID, "client": client}, nil
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
	port, mapped := server.PublicPort(address, node.ListenPort)
	if rawPort := r.URL.Query().Get("port"); rawPort != "" {
		parsed, err := strconv.ParseUint(rawPort, 10, 16)
		if err != nil || parsed == 0 {
			writeError(w, http.StatusBadRequest, "invalid published port")
			return
		}
		port, mapped = uint16(parsed), true
	}
	if !mapped {
		writeError(w, http.StatusConflict, fmt.Sprintf("端口 %d 不在 %s 的端口映射内，无法经公网 IPv4 连接", node.ListenPort, server.Name))
		return
	}
	name := valueOr(r.URL.Query().Get("name"), node.Name)
	client, err := configgen.ExportClient(node, configgen.PublishedEndpoint{Address: address, Port: port, Name: name})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, client)
}

// nodeSettings are the parameters an administrator chooses for a node. The
// panel generates the credentials, so they are not part of it.
type nodeSettings struct {
	Name        string              `json:"name"`
	ListenPort  uint16              `json:"listen_port"`
	Reality     realitySettings     `json:"reality,omitzero"`
	Shadowsocks shadowsocksSettings `json:"shadowsocks,omitzero"`
	Snell       snellSettings       `json:"snell,omitzero"`
	ShadowTLS   *shadowTLSSettings  `json:"shadow_tls,omitempty"`
}

type realitySettings struct {
	Flow              string            `json:"flow"`
	HandshakeServer   string            `json:"handshake_server"`
	HandshakePort     uint16            `json:"handshake_port"`
	ServerName        string            `json:"server_name"`
	Fingerprint       string            `json:"fingerprint"`
	Transport         string            `json:"transport"`
	TransportSettings map[string]string `json:"transport_settings,omitempty"`
	MaxTimeDifference string            `json:"max_time_difference,omitempty"`
}

type shadowsocksSettings struct {
	Method        string `json:"method"`
	AllowInsecure bool   `json:"allow_insecure"`
}

type snellSettings struct {
	Version       int    `json:"version"`
	ObfsMode      string `json:"obfs_mode,omitempty"`
	Mode          string `json:"mode,omitempty"`
	AllowInsecure bool   `json:"allow_insecure"`
}

type shadowTLSSettings struct {
	HandshakeServer string `json:"handshake_server"`
	HandshakePort   uint16 `json:"handshake_port"`
}

type createNodeRequest struct {
	ServerID string         `json:"server_id"`
	Protocol model.Protocol `json:"protocol"`
	nodeSettings
}

type updateNodeRequest struct {
	nodeSettings
	// RotateCredentials replaces every key, so existing client links stop working.
	RotateCredentials bool `json:"rotate_credentials"`
}

type nodeView struct {
	ID       string         `json:"id"`
	ServerID string         `json:"server_id"`
	Protocol model.Protocol `json:"protocol"`
	Managed  bool           `json:"managed"`
	nodeSettings
}

func viewNode(node model.Node) nodeView {
	view := nodeView{ID: node.ID, ServerID: node.ServerID, Protocol: node.Protocol, Managed: node.Managed}
	view.Name, view.ListenPort = node.Name, node.ListenPort
	if spec := node.Reality; spec != nil {
		view.Reality = realitySettings{Flow: spec.Flow, HandshakeServer: spec.HandshakeServer, HandshakePort: spec.HandshakePort,
			ServerName: spec.ServerName, Fingerprint: spec.Fingerprint, Transport: spec.Transport,
			TransportSettings: spec.TransportSettings, MaxTimeDifference: spec.MaxTimeDifference}
	}
	if spec := node.SS; spec != nil {
		view.Shadowsocks = shadowsocksSettings{Method: spec.Method, AllowInsecure: spec.AllowInsecure}
	}
	if spec := node.Snell; spec != nil {
		view.Snell = snellSettings{Version: spec.Version, ObfsMode: spec.ObfsMode, Mode: spec.Mode, AllowInsecure: spec.AllowInsecure}
	}
	if spec := node.ShadowTLS; spec != nil {
		view.ShadowTLS = &shadowTLSSettings{HandshakeServer: spec.HandshakeServer, HandshakePort: spec.HandshakePort}
	}
	return view
}
