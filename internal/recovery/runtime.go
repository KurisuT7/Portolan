package recovery

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/KurisuT7/Portolan/internal/model"
)

const planSchema = 1

var (
	managedID      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,96}$`)
	runtimeShortID = regexp.MustCompile(`^(?:[0-9a-fA-F]{2}){0,8}$`)
)

type Plan struct {
	Schema  int          `json:"schema"`
	Servers []ServerPlan `json:"servers"`
}

type ServerPlan struct {
	AgentConfig    string                     `json:"agent_config"`
	RuntimeRelease string                     `json:"runtime_release"`
	Server         model.Server               `json:"server"`
	Nodes          map[string]NodeMetadata    `json:"nodes"`
	Forwards       map[string]ForwardMetadata `json:"forwards"`
}

type NodeMetadata struct {
	Name              *string            `json:"name"`
	Profile           *string            `json:"profile"`
	PublicKey         *string            `json:"public_key,omitempty"`
	Fingerprint       *string            `json:"fingerprint,omitempty"`
	Transport         *string            `json:"transport,omitempty"`
	TransportSettings *map[string]string `json:"transport_settings,omitempty"`
	AllowInsecure     *bool              `json:"allow_insecure,omitempty"`
}

type ForwardMetadata struct {
	Name           *string `json:"name"`
	TargetServerID *string `json:"target_server_id"`
	TargetNodeID   *string `json:"target_node_id"`
}

type RecoveredNode struct {
	Node    model.Node
	Profile string
}

type Result struct {
	Servers  []model.Server
	Nodes    []RecoveredNode
	Forwards []model.Forward
}

type rawPlan struct {
	Schema  int               `json:"schema"`
	Servers []json.RawMessage `json:"servers"`
}

type rawServerPlan struct {
	AgentConfig    string                     `json:"agent_config"`
	RuntimeRelease string                     `json:"runtime_release"`
	Server         json.RawMessage            `json:"server"`
	Nodes          map[string]NodeMetadata    `json:"nodes"`
	Forwards       map[string]ForwardMetadata `json:"forwards"`
}

type agentIdentity struct {
	ServerID string `json:"server_id"`
}

type runtimeManifest struct {
	Revision  int64  `json:"revision"`
	Nodes     int    `json:"nodes"`
	Forwards  int    `json:"forwards"`
	CreatedAt string `json:"created_at"`
	// Listeners lets the Agent verify services; recovery does not use them.
	Listeners map[string][]struct {
		Network string `json:"network"`
		Port    uint16 `json:"port"`
	} `json:"listeners"`
}

type singFragment struct {
	Inbounds []singInbound `json:"inbounds"`
}

type singInbound struct {
	Type            string          `json:"type"`
	Tag             string          `json:"tag"`
	Listen          string          `json:"listen"`
	ListenPort      uint16          `json:"listen_port"`
	Users           []singUser      `json:"users,omitempty"`
	TLS             *singTLS        `json:"tls,omitempty"`
	Transport       json.RawMessage `json:"transport,omitempty"`
	Method          string          `json:"method,omitempty"`
	Password        string          `json:"password,omitempty"`
	Version         int             `json:"version,omitempty"`
	PSK             string          `json:"psk,omitempty"`
	ObfsMode        string          `json:"obfs_mode,omitempty"`
	Mode            string          `json:"mode,omitempty"`
	Network         []string        `json:"network,omitempty"`
	OverrideAddress string          `json:"override_address,omitempty"`
	OverridePort    uint16          `json:"override_port,omitempty"`
}

type singUser struct {
	Name string `json:"name"`
	UUID string `json:"uuid"`
	Flow string `json:"flow,omitempty"`
}

type singTLS struct {
	Enabled    bool        `json:"enabled"`
	ServerName string      `json:"server_name"`
	Reality    singReality `json:"reality"`
}

type singReality struct {
	Enabled           bool          `json:"enabled"`
	PrivateKey        string        `json:"private_key"`
	ShortIDs          []string      `json:"short_id"`
	Handshake         singHandshake `json:"handshake"`
	MaxTimeDifference string        `json:"max_time_difference,omitempty"`
}

type singHandshake struct {
	Server     string `json:"server"`
	ServerPort uint16 `json:"server_port"`
}

func Load(planPath string) (Result, error) {
	absolute, err := filepath.Abs(planPath)
	if err != nil {
		return Result{}, err
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return Result{}, err
	}
	var raw rawPlan
	if err := decodeStrict(data, &raw); err != nil {
		return Result{}, fmt.Errorf("decode recovery plan: %w", err)
	}
	if raw.Schema != planSchema {
		return Result{}, fmt.Errorf("unsupported recovery plan schema %d", raw.Schema)
	}
	if len(raw.Servers) == 0 {
		return Result{}, errors.New("recovery plan must contain at least one server")
	}
	base := filepath.Dir(absolute)
	plan := Plan{Schema: raw.Schema}
	for index, encoded := range raw.Servers {
		server, err := decodeServerPlan(encoded, base)
		if err != nil {
			return Result{}, fmt.Errorf("server plan %d: %w", index, err)
		}
		plan.Servers = append(plan.Servers, server)
	}
	return recoverPlan(plan)
}

func decodeServerPlan(data []byte, base string) (ServerPlan, error) {
	var raw rawServerPlan
	if err := decodeStrict(data, &raw); err != nil {
		return ServerPlan{}, err
	}
	if strings.TrimSpace(raw.AgentConfig) == "" || strings.TrimSpace(raw.RuntimeRelease) == "" {
		return ServerPlan{}, errors.New("agent_config and runtime_release are required")
	}
	var fields map[string]json.RawMessage
	if err := decodeStrict(raw.Server, &fields); err != nil {
		return ServerPlan{}, fmt.Errorf("decode server metadata: %w", err)
	}
	required := []string{"id", "name", "address", "ipv4_address", "ipv6_address", "egress_ipv4", "egress_ipv6", "region"}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return ServerPlan{}, fmt.Errorf("server metadata must explicitly contain %q", name)
		}
	}
	for name := range fields {
		if !slices.Contains(required, name) {
			return ServerPlan{}, fmt.Errorf("unknown server metadata field %q", name)
		}
	}
	var server model.Server
	if err := decodeStrict(raw.Server, &server); err != nil {
		return ServerPlan{}, fmt.Errorf("decode server: %w", err)
	}
	return ServerPlan{
		AgentConfig:    resolvePlanPath(base, raw.AgentConfig),
		RuntimeRelease: resolvePlanPath(base, raw.RuntimeRelease),
		Server:         server,
		Nodes:          raw.Nodes,
		Forwards:       raw.Forwards,
	}, nil
}

func recoverPlan(plan Plan) (Result, error) {
	var result Result
	serverIDs := map[string]bool{}
	nodeIDs := map[string]bool{}
	serversByID := map[string]model.Server{}
	nodesByID := map[string]model.Node{}
	forwardIDs := map[string]bool{}
	ports := map[string]string{}

	for _, entry := range plan.Servers {
		if !managedID.MatchString(entry.Server.ID) {
			return Result{}, fmt.Errorf("server ID %q is invalid", entry.Server.ID)
		}
		if serverIDs[entry.Server.ID] {
			return Result{}, fmt.Errorf("duplicate server ID %q", entry.Server.ID)
		}
		serverIDs[entry.Server.ID] = true
		serversByID[entry.Server.ID] = entry.Server
		if err := entry.Server.Validate(); err != nil {
			return Result{}, fmt.Errorf("server %s: %w", entry.Server.ID, err)
		}
		identity, err := loadAgentIdentity(entry.AgentConfig)
		if err != nil {
			return Result{}, fmt.Errorf("server %s agent config: %w", entry.Server.ID, err)
		}
		if identity.ServerID != entry.Server.ID {
			return Result{}, fmt.Errorf("server %s does not match agent config server ID %s", entry.Server.ID, identity.ServerID)
		}

		nodes, forwards, err := recoverRelease(entry)
		if err != nil {
			return Result{}, fmt.Errorf("server %s runtime: %w", entry.Server.ID, err)
		}
		result.Servers = append(result.Servers, entry.Server)
		for _, recovered := range nodes {
			if nodeIDs[recovered.Node.ID] {
				return Result{}, fmt.Errorf("duplicate node ID %q", recovered.Node.ID)
			}
			nodeIDs[recovered.Node.ID] = true
			nodesByID[recovered.Node.ID] = recovered.Node
			key := recovered.Node.ServerID + ":" + strconv.Itoa(int(recovered.Node.ListenPort))
			if owner := ports[key]; owner != "" {
				return Result{}, fmt.Errorf("listen port collision on %s between %s and node %s", key, owner, recovered.Node.ID)
			}
			ports[key] = "node " + recovered.Node.ID
			result.Nodes = append(result.Nodes, recovered)
		}
		for _, forward := range forwards {
			if forwardIDs[forward.ID] {
				return Result{}, fmt.Errorf("duplicate forward ID %q", forward.ID)
			}
			forwardIDs[forward.ID] = true
			key := forward.IngressServerID + ":" + strconv.Itoa(int(forward.ListenPort))
			if owner := ports[key]; owner != "" {
				return Result{}, fmt.Errorf("listen port collision on %s between %s and forward %s", key, owner, forward.ID)
			}
			ports[key] = "forward " + forward.ID
			result.Forwards = append(result.Forwards, forward)
		}
	}
	for _, forward := range result.Forwards {
		if err := validateForwardAssociation(forward, serversByID, nodesByID); err != nil {
			return Result{}, err
		}
	}
	return result, nil
}

func validateForwardAssociation(forward model.Forward, servers map[string]model.Server, nodes map[string]model.Node) error {
	ingress, ok := servers[forward.IngressServerID]
	if !ok {
		return fmt.Errorf("forward %s references unknown ingress server %s", forward.ID, forward.IngressServerID)
	}
	if forward.TargetNodeID != "" {
		node, ok := nodes[forward.TargetNodeID]
		if !ok {
			return fmt.Errorf("forward %s references unknown target node %s", forward.ID, forward.TargetNodeID)
		}
		if forward.TargetServerID != node.ServerID {
			return fmt.Errorf("forward %s target node %s belongs to server %s, not explicit target server %s", forward.ID, node.ID, node.ServerID, forward.TargetServerID)
		}
		if forward.TargetPort != node.ListenPort {
			return fmt.Errorf("forward %s target port %d does not match target node %s port %d", forward.ID, forward.TargetPort, node.ID, node.ListenPort)
		}
	}
	if forward.TargetServerID == "" {
		return nil
	}
	target, ok := servers[forward.TargetServerID]
	if !ok {
		return fmt.Errorf("forward %s references unknown target server %s", forward.ID, forward.TargetServerID)
	}
	expectedHost := model.SelectServerTargetAddress(ingress, target)
	if expectedHost == "" {
		return fmt.Errorf("forward %s target server %s has no address derivable from the explicit recovery metadata", forward.ID, target.ID)
	}
	if model.NormalizeHost(forward.TargetHost) != model.NormalizeHost(expectedHost) {
		return fmt.Errorf("forward %s runtime target host %s does not match target server %s selected address %s", forward.ID, forward.TargetHost, target.ID, expectedHost)
	}
	return nil
}

func recoverRelease(entry ServerPlan) ([]RecoveredNode, []model.Forward, error) {
	release, err := filepath.EvalSymlinks(entry.RuntimeRelease)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve runtime release: %w", err)
	}
	if err := requireDirectory(release); err != nil {
		return nil, nil, fmt.Errorf("runtime release: %w", err)
	}
	manifestPath := filepath.Join(release, "manifest.json")
	if err := requireRegularFile(manifestPath); err != nil {
		return nil, nil, fmt.Errorf("runtime manifest: %w", err)
	}
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read manifest: %w", err)
	}
	var manifest runtimeManifest
	if err := decodeStrict(manifestData, &manifest); err != nil {
		return nil, nil, fmt.Errorf("decode manifest: %w", err)
	}
	if manifest.Revision <= 0 || manifest.Nodes < 0 || manifest.Forwards < 0 || strings.TrimSpace(manifest.CreatedAt) == "" {
		return nil, nil, errors.New("runtime manifest counts or revision are invalid")
	}

	confDir := filepath.Join(release, "sing-box", "conf.d")
	if err := requireDirectory(confDir); err != nil {
		return nil, nil, fmt.Errorf("sing-box fragments: %w", err)
	}
	entries, err := os.ReadDir(confDir)
	if err != nil {
		return nil, nil, fmt.Errorf("read sing-box fragments: %w", err)
	}
	var nodes []RecoveredNode
	var forwards []model.Forward
	seenNodeMeta := map[string]bool{}
	seenForwardMeta := map[string]bool{}
	for _, file := range entries {
		info, infoErr := file.Info()
		if infoErr != nil || !info.Mode().IsRegular() || file.Type()&os.ModeSymlink != 0 {
			return nil, nil, fmt.Errorf("unexpected non-regular entry in conf.d: %s", file.Name())
		}
		name := file.Name()
		switch {
		case strings.HasPrefix(name, "10-node-") && strings.HasSuffix(name, ".json"):
			id := strings.TrimSuffix(strings.TrimPrefix(name, "10-node-"), ".json")
			metadata, ok := entry.Nodes[id]
			if !ok {
				return nil, nil, fmt.Errorf("node %s is missing explicit recovery metadata", id)
			}
			node, profile, err := parseNode(filepath.Join(confDir, name), id, entry.Server.ID, metadata)
			if err != nil {
				return nil, nil, err
			}
			seenNodeMeta[id] = true
			nodes = append(nodes, RecoveredNode{Node: node, Profile: profile})
		case strings.HasPrefix(name, "20-forward-") && strings.HasSuffix(name, ".json"):
			id := strings.TrimSuffix(strings.TrimPrefix(name, "20-forward-"), ".json")
			metadata, ok := entry.Forwards[id]
			if !ok {
				return nil, nil, fmt.Errorf("forward %s is missing explicit recovery metadata", id)
			}
			forward, err := parseSingForward(filepath.Join(confDir, name), id, entry.Server.ID, metadata)
			if err != nil {
				return nil, nil, err
			}
			seenForwardMeta[id] = true
			forwards = append(forwards, forward)
		default:
			return nil, nil, fmt.Errorf("unexpected managed fragment %q", name)
		}
	}
	realmDir := filepath.Join(release, "realm")
	if err := requireDirectory(realmDir); err != nil {
		return nil, nil, fmt.Errorf("Realm fragments: %w", err)
	}
	realmEntries, err := os.ReadDir(realmDir)
	if err != nil {
		return nil, nil, fmt.Errorf("read Realm fragments: %w", err)
	}
	for _, file := range realmEntries {
		info, infoErr := file.Info()
		if infoErr != nil || !info.Mode().IsRegular() || file.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(file.Name(), ".toml") {
			return nil, nil, fmt.Errorf("unexpected Realm entry %q", file.Name())
		}
		id := strings.TrimSuffix(file.Name(), ".toml")
		metadata, ok := entry.Forwards[id]
		if !ok {
			return nil, nil, fmt.Errorf("Realm forward %s is missing explicit recovery metadata", id)
		}
		forward, err := parseRealmForward(filepath.Join(realmDir, file.Name()), id, entry.Server.ID, metadata)
		if err != nil {
			return nil, nil, err
		}
		seenForwardMeta[id] = true
		forwards = append(forwards, forward)
	}
	if manifest.Nodes != len(nodes) || manifest.Forwards != len(forwards) {
		return nil, nil, fmt.Errorf("runtime manifest reports %d nodes/%d forwards but only %d/%d active configurations are recoverable; disabled or missing objects cannot be guessed",
			manifest.Nodes, manifest.Forwards, len(nodes), len(forwards))
	}
	for id := range entry.Nodes {
		if !seenNodeMeta[id] {
			return nil, nil, fmt.Errorf("node metadata %s has no runtime fragment", id)
		}
	}
	for id := range entry.Forwards {
		if !seenForwardMeta[id] {
			return nil, nil, fmt.Errorf("forward metadata %s has no runtime fragment", id)
		}
	}
	return nodes, forwards, nil
}

func parseNode(path, id, serverID string, metadata NodeMetadata) (model.Node, string, error) {
	if !managedID.MatchString(id) {
		return model.Node{}, "", fmt.Errorf("node ID %q is unsafe", id)
	}
	if metadata.Name == nil || strings.TrimSpace(*metadata.Name) == "" || metadata.Profile == nil || strings.TrimSpace(*metadata.Profile) == "" {
		return model.Node{}, "", fmt.Errorf("node %s requires explicit non-empty name and profile metadata", id)
	}
	inbound, err := loadInbound(path)
	if err != nil {
		return model.Node{}, "", fmt.Errorf("node %s: %w", id, err)
	}
	if inbound.Tag != "node-"+id || inbound.Listen != "::" || inbound.ListenPort == 0 {
		return model.Node{}, "", fmt.Errorf("node %s tag or listener does not match its runtime filename", id)
	}
	node := model.Node{ID: id, ServerID: serverID, Name: *metadata.Name, ListenPort: inbound.ListenPort, Enabled: true, Managed: true}
	switch inbound.Type {
	case "vless":
		node.Protocol = model.ProtocolReality
		if len(inbound.Users) != 1 || inbound.TLS == nil || !inbound.TLS.Enabled || !inbound.TLS.Reality.Enabled {
			return model.Node{}, "", fmt.Errorf("node %s Reality runtime is incomplete", id)
		}
		if inbound.Users[0].Name != node.Name {
			return model.Node{}, "", fmt.Errorf("node %s metadata name does not match the runtime user name", id)
		}
		if metadata.PublicKey == nil || strings.TrimSpace(*metadata.PublicKey) == "" || metadata.Fingerprint == nil || strings.TrimSpace(*metadata.Fingerprint) == "" {
			return model.Node{}, "", fmt.Errorf("node %s requires explicit Reality public_key and fingerprint metadata", id)
		}
		derived, err := realityPublicKey(inbound.TLS.Reality.PrivateKey)
		if err != nil {
			return model.Node{}, "", fmt.Errorf("node %s Reality private key: %w", id, err)
		}
		if derived != *metadata.PublicKey {
			return model.Node{}, "", fmt.Errorf("node %s Reality public key does not match its runtime private key", id)
		}
		for _, shortID := range inbound.TLS.Reality.ShortIDs {
			if !runtimeShortID.MatchString(shortID) {
				return model.Node{}, "", fmt.Errorf("node %s Reality runtime contains an invalid short ID", id)
			}
		}
		if metadata.Transport == nil || metadata.TransportSettings == nil {
			return model.Node{}, "", fmt.Errorf("node %s requires explicit Reality transport and transport_settings metadata", id)
		}
		transport, settings, err := parseTransport(inbound.Transport)
		if err != nil {
			return model.Node{}, "", fmt.Errorf("node %s transport: %w", id, err)
		}
		if canonicalTransport(transport) != canonicalTransport(*metadata.Transport) {
			return model.Node{}, "", fmt.Errorf("node %s transport metadata does not match its runtime", id)
		}
		for key, value := range settings {
			if recovered, ok := (*metadata.TransportSettings)[key]; !ok || recovered != value {
				return model.Node{}, "", fmt.Errorf("node %s transport setting %s does not match its runtime", id, key)
			}
		}
		if len(*metadata.TransportSettings) != len(settings) {
			return model.Node{}, "", fmt.Errorf("node %s transport_settings must exactly match its runtime; client-only values cannot be verified", id)
		}
		for key := range *metadata.TransportSettings {
			if !slices.Contains([]string{"path", "host", "service_name"}, key) {
				return model.Node{}, "", fmt.Errorf("node %s transport metadata contains unsupported field %s", id, key)
			}
		}
		node.Reality = &model.RealitySpec{
			UUID: inbound.Users[0].UUID, Flow: inbound.Users[0].Flow,
			HandshakeServer: inbound.TLS.Reality.Handshake.Server,
			HandshakePort:   inbound.TLS.Reality.Handshake.ServerPort,
			ServerName:      inbound.TLS.ServerName,
			PrivateKey:      inbound.TLS.Reality.PrivateKey, PublicKey: *metadata.PublicKey,
			ShortIDs: inbound.TLS.Reality.ShortIDs, Fingerprint: *metadata.Fingerprint,
			Transport: *metadata.Transport, TransportSettings: *metadata.TransportSettings,
			MaxTimeDifference: inbound.TLS.Reality.MaxTimeDifference,
		}
	case "shadowsocks":
		node.Protocol = model.ProtocolShadowsocks
		if metadata.AllowInsecure == nil {
			return model.Node{}, "", fmt.Errorf("node %s requires explicit allow_insecure metadata", id)
		}
		node.SS = &model.SSSpec{Method: inbound.Method, Password: inbound.Password, AllowInsecure: *metadata.AllowInsecure}
	case "snell":
		node.Protocol = model.ProtocolSnell
		if metadata.AllowInsecure == nil {
			return model.Node{}, "", fmt.Errorf("node %s requires explicit allow_insecure metadata", id)
		}
		node.Snell = &model.SnellSpec{Version: inbound.Version, PSK: inbound.PSK, ObfsMode: inbound.ObfsMode, Mode: inbound.Mode, AllowInsecure: *metadata.AllowInsecure}
	default:
		return model.Node{}, "", fmt.Errorf("node %s uses unsupported inbound type %q", id, inbound.Type)
	}
	if err := node.Validate(); err != nil {
		return model.Node{}, "", fmt.Errorf("node %s: %w", id, err)
	}
	return node, *metadata.Profile, nil
}

func parseSingForward(path, id, serverID string, metadata ForwardMetadata) (model.Forward, error) {
	inbound, err := loadInbound(path)
	if err != nil {
		return model.Forward{}, fmt.Errorf("forward %s: %w", id, err)
	}
	if inbound.Type != "direct" || inbound.Tag != "forward-"+id || inbound.Listen != "::" {
		return model.Forward{}, fmt.Errorf("forward %s runtime identity is invalid", id)
	}
	return finishForward(id, serverID, inbound.ListenPort, inbound.Network, inbound.OverrideAddress, inbound.OverridePort, model.ForwardSingBox, metadata)
}

func parseRealmForward(path, id, serverID string, metadata ForwardMetadata) (model.Forward, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return model.Forward{}, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "[") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return model.Forward{}, fmt.Errorf("Realm forward %s has an unsupported line", id)
		}
		key := strings.TrimSpace(parts[0])
		if _, duplicate := values[key]; duplicate {
			return model.Forward{}, fmt.Errorf("Realm forward %s repeats %s", id, key)
		}
		values[key] = strings.TrimSpace(parts[1])
	}
	required := []string{"level", "no_tcp", "use_udp", "ipv6_only", "listen", "remote"}
	for _, key := range required {
		if _, ok := values[key]; !ok {
			return model.Forward{}, fmt.Errorf("Realm forward %s is missing %s", id, key)
		}
	}
	for key := range values {
		if !slices.Contains(required, key) {
			return model.Forward{}, fmt.Errorf("Realm forward %s contains unknown field %s", id, key)
		}
	}
	if values["level"] != `"warn"` || values["ipv6_only"] != "false" {
		return model.Forward{}, fmt.Errorf("Realm forward %s has unsupported log or IPv6-only settings", id)
	}
	noTCP, err := strconv.ParseBool(values["no_tcp"])
	if err != nil {
		return model.Forward{}, err
	}
	useUDP, err := strconv.ParseBool(values["use_udp"])
	if err != nil {
		return model.Forward{}, err
	}
	listen, err := strconv.Unquote(values["listen"])
	if err != nil {
		return model.Forward{}, err
	}
	remote, err := strconv.Unquote(values["remote"])
	if err != nil {
		return model.Forward{}, err
	}
	listenHost, listenPortText, err := net.SplitHostPort(listen)
	if err != nil || listenHost != "::" {
		return model.Forward{}, fmt.Errorf("Realm forward %s listener is invalid", id)
	}
	remoteHost, remotePortText, err := net.SplitHostPort(remote)
	if err != nil {
		return model.Forward{}, fmt.Errorf("Realm forward %s target is invalid", id)
	}
	listenPort, err := parsePort(listenPortText)
	if err != nil {
		return model.Forward{}, err
	}
	remotePort, err := parsePort(remotePortText)
	if err != nil {
		return model.Forward{}, err
	}
	var networks []string
	if !noTCP {
		networks = append(networks, "tcp")
	}
	if useUDP {
		networks = append(networks, "udp")
	}
	return finishForward(id, serverID, listenPort, networks, remoteHost, remotePort, model.ForwardRealm, metadata)
}

func finishForward(id, serverID string, listenPort uint16, networks []string, targetHost string, targetPort uint16, engine model.ForwardEngine, metadata ForwardMetadata) (model.Forward, error) {
	if !managedID.MatchString(id) {
		return model.Forward{}, fmt.Errorf("forward ID %q is unsafe", id)
	}
	if metadata.Name == nil || strings.TrimSpace(*metadata.Name) == "" || metadata.TargetServerID == nil || metadata.TargetNodeID == nil {
		return model.Forward{}, fmt.Errorf("forward %s requires explicit name, target_server_id and target_node_id metadata (empty IDs must be intentional)", id)
	}
	forward := model.Forward{
		ID: id, IngressServerID: serverID, Name: *metadata.Name,
		ListenPort: listenPort, Networks: networks, TargetHost: targetHost, TargetPort: targetPort,
		TargetServerID: *metadata.TargetServerID, TargetNodeID: *metadata.TargetNodeID,
		Engine: engine, Enabled: true,
	}
	if err := forward.Validate(); err != nil {
		return model.Forward{}, fmt.Errorf("forward %s: %w", id, err)
	}
	return forward, nil
}

func loadInbound(path string) (singInbound, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return singInbound{}, err
	}
	var fragment singFragment
	if err := decodeStrict(data, &fragment); err != nil {
		return singInbound{}, err
	}
	if len(fragment.Inbounds) != 1 {
		return singInbound{}, errors.New("fragment must contain exactly one inbound")
	}
	return fragment.Inbounds[0], nil
}

func loadAgentIdentity(path string) (agentIdentity, error) {
	if err := requireRegularFile(path); err != nil {
		return agentIdentity{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return agentIdentity{}, err
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(data, &value); err != nil {
		return agentIdentity{}, err
	}
	raw, ok := value["server_id"]
	if !ok {
		return agentIdentity{}, errors.New("server_id is missing")
	}
	var identity agentIdentity
	if err := json.Unmarshal(raw, &identity.ServerID); err != nil || !managedID.MatchString(identity.ServerID) {
		return agentIdentity{}, errors.New("server_id is invalid")
	}
	return identity, nil
}

func requireRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("must be a regular file: %s", path)
	}
	return nil
}

func requireDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("must be a real directory: %s", path)
	}
	return nil
}

func parseTransport(data json.RawMessage) (string, map[string]string, error) {
	if len(data) == 0 || string(data) == "null" {
		return "tcp", nil, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return "", nil, err
	}
	var transport string
	if raw, ok := values["type"]; ok {
		if err := json.Unmarshal(raw, &transport); err != nil {
			return "", nil, err
		}
		delete(values, "type")
	}
	settings := map[string]string{}
	for key, raw := range values {
		switch key {
		case "path", "service_name":
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				return "", nil, err
			}
			settings[key] = value
		case "host":
			var hosts []string
			if err := json.Unmarshal(raw, &hosts); err != nil {
				return "", nil, err
			}
			settings[key] = strings.Join(hosts, ",")
		default:
			return "", nil, fmt.Errorf("unknown transport field %q", key)
		}
	}
	return transport, settings, nil
}

func canonicalTransport(value string) string {
	if value == "" {
		return "tcp"
	}
	return value
}

func realityPublicKey(private string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(private)
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(private)
	}
	if err != nil {
		return "", err
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

func parsePort(value string) (uint16, error) {
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil || port == 0 {
		return 0, fmt.Errorf("invalid port %q", value)
	}
	return uint16(port), nil
}

func resolvePlanPath(base, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(base, filepath.FromSlash(value))
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
