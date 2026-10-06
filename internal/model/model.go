package model

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"
)

type Protocol string

const (
	ProtocolReality     Protocol = "vless-reality"
	ProtocolShadowsocks Protocol = "shadowsocks"
	ProtocolSnell       Protocol = "snell"
)

var ShadowsocksMethods = []string{
	"2022-blake3-aes-128-gcm",
	"2022-blake3-aes-256-gcm",
	"2022-blake3-chacha20-poly1305",
	"aes-128-gcm",
	"aes-192-gcm",
	"aes-256-gcm",
	"chacha20-ietf-poly1305",
	"xchacha20-ietf-poly1305",
	"none",
}

var (
	hostPattern      = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	shortIDPattern   = regexp.MustCompile(`^(?:[0-9a-fA-F]{2}){0,8}$`)
	sharedIPv4       = netip.MustParsePrefix("100.64.0.0/10")
	unitNamePattern  = regexp.MustCompile(`^portolan-(?:sing-box|realm@[a-zA-Z0-9_-]{1,96})\.service$`)
	unitStatePattern = regexp.MustCompile(`^[a-z-]{1,32}$`)
	versionPattern   = regexp.MustCompile(`^[0-9A-Za-z.+-]{0,64}$`)
)

type Server struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Address     string         `json:"address"`
	IPv4Address string         `json:"ipv4_address,omitempty"`
	IPv6Address string         `json:"ipv6_address,omitempty"`
	EgressIPv4  bool           `json:"egress_ipv4"`
	EgressIPv6  bool           `json:"egress_ipv6"`
	Region      string         `json:"region,omitempty"`
	AgentStatus string         `json:"agent_status"`
	LastSeenAt  time.Time      `json:"last_seen_at,omitempty"`
	Runtime     *RuntimeStatus `json:"runtime,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}

// RuntimeStatus is what an Agent observes on its own host: the desired-state
// revision of the active release, its own and the installed core versions,
// and the systemd state of every service that release defines.
type RuntimeStatus struct {
	AppliedRevision int64        `json:"applied_revision"`
	AgentVersion    string       `json:"agent_version"`
	SingBoxVersion  string       `json:"sing_box_version"`
	RealmVersion    string       `json:"realm_version"`
	Units           []UnitStatus `json:"units"`
	ReportedAt      time.Time    `json:"reported_at,omitzero"`
}

type UnitStatus struct {
	Name        string `json:"name"`
	ActiveState string `json:"active_state"`
	SubState    string `json:"sub_state"`
}

// Core is a proxy runtime the Agent runs. Its version is chosen in the panel.
type Core string

const (
	CoreSingBox Core = "sing-box"
	CoreRealm   Core = "realm"
)

func (c Core) Valid() bool { return c == CoreSingBox || c == CoreRealm }

// JobType names the Agent job that installs this core.
func (c Core) JobType() string { return "update-" + string(c) }

// CoreTarget is the release new installs receive and updates converge to,
// with the SHA-256 of each Linux archive by GOARCH.
type CoreTarget struct {
	Core      Core              `json:"core"`
	Version   string            `json:"version"`
	SHA256    map[string]string `json:"sha256"`
	UpdatedAt time.Time         `json:"updated_at"`
}

func (r RuntimeStatus) Validate() error {
	if r.AppliedRevision < 0 {
		return errors.New("applied revision is invalid")
	}
	if !versionPattern.MatchString(r.AgentVersion) || !versionPattern.MatchString(r.SingBoxVersion) || !versionPattern.MatchString(r.RealmVersion) {
		return errors.New("runtime version is invalid")
	}
	if r.Units == nil || len(r.Units) > 256 {
		return errors.New("runtime units must be a list of at most 256 entries")
	}
	for _, unit := range r.Units {
		if !unitNamePattern.MatchString(unit.Name) || !unitStatePattern.MatchString(unit.ActiveState) || !unitStatePattern.MatchString(unit.SubState) {
			return fmt.Errorf("runtime unit %q is invalid", unit.Name)
		}
	}
	return nil
}

func (s Server) Validate() error {
	name := strings.TrimSpace(s.Name)
	address := strings.TrimSpace(s.Address)
	if name == "" || len(name) > 96 {
		return errors.New("server name must contain 1 to 96 characters")
	}
	if address != "" && (len(address) > 253 || (net.ParseIP(address) == nil && !hostPattern.MatchString(address))) {
		return errors.New("server address must be an IP address or hostname")
	}
	if s.IPv4Address != "" && (!IsPublicRoutableIP(s.IPv4Address) || net.ParseIP(s.IPv4Address).To4() == nil) {
		return errors.New("server IPv4 address must be a publicly routable IPv4 address")
	}
	if s.IPv6Address != "" && (!IsPublicRoutableIP(s.IPv6Address) || net.ParseIP(s.IPv6Address).To4() != nil) {
		return errors.New("server IPv6 address must be a publicly routable IPv6 address")
	}
	if len(s.Region) > 32 {
		return errors.New("server region must not exceed 32 characters")
	}
	return nil
}

// IsPublicRoutableIP reports whether an address is suitable as an automatically
// discovered public ingress endpoint. In particular, 100.64.0.0/10 is shared
// carrier-grade NAT space (and is also used by Tailscale), not a public IPv4
// endpoint even though the standard library classifies it as global unicast.
func IsPublicRoutableIP(value string) bool {
	address, err := netip.ParseAddr(NormalizeHost(value))
	if err != nil {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	return !sharedIPv4.Contains(address)
}

// SelectServerTargetAddress chooses a target public ingress endpoint using the
// entry server's outbound stack. Public ingress addresses and outbound
// capabilities are deliberately separate: a server behind CGNAT can have no
// public IPv4 ingress while still reaching IPv4 targets. A hostname remains an
// explicit operator override.
func SelectServerTargetAddress(ingress, target Server) string {
	configured := NormalizeHost(target.Address)
	if configured != "" && net.ParseIP(configured) == nil {
		return configured
	}

	targetIPv4 := publicAddressForFamily(target.IPv4Address, true)
	targetIPv6 := publicAddressForFamily(target.IPv6Address, false)
	configuredIPv4 := ""
	configuredIPv6 := ""
	if IsPublicRoutableIP(configured) {
		if net.ParseIP(configured).To4() != nil {
			configuredIPv4 = configured
			if targetIPv4 == "" {
				targetIPv4 = configured
			}
		} else {
			configuredIPv6 = configured
			if targetIPv6 == "" {
				targetIPv6 = configured
			}
		}
	}

	egressIPv4 := ingress.EgressIPv4
	egressIPv6 := ingress.EgressIPv6
	egressFamilyKnown := egressIPv4 || egressIPv6
	if !egressFamilyKnown {
		// Backward compatibility for Agents that predate explicit egress
		// reporting. A public ingress address necessarily implies that the
		// corresponding local stack exists, but the inverse is not true.
		egressIPv4 = publicAddressForFamily(ingress.IPv4Address, true) != ""
		egressIPv6 = publicAddressForFamily(ingress.IPv6Address, false) != ""
		egressFamilyKnown = egressIPv4 || egressIPv6
	}
	if configuredIPv4 != "" && (!egressFamilyKnown || egressIPv4) {
		return configuredIPv4
	}
	if configuredIPv6 != "" && (!egressFamilyKnown || egressIPv6) {
		return configuredIPv6
	}
	if targetIPv4 != "" && (!egressFamilyKnown || egressIPv4) {
		return targetIPv4
	}
	if targetIPv6 != "" && (!egressFamilyKnown || egressIPv6) {
		return targetIPv6
	}
	if targetIPv4 != "" {
		return targetIPv4
	}
	if targetIPv6 != "" {
		return targetIPv6
	}
	return configured
}

func publicAddressForFamily(value string, ipv4 bool) string {
	value = NormalizeHost(value)
	ip := net.ParseIP(value)
	if !IsPublicRoutableIP(value) || ip == nil || (ip.To4() != nil) != ipv4 {
		return ""
	}
	return ip.String()
}

// ForwardProbe is measured by the ingress Agent. It describes reachability
// from the machine that actually carries traffic, rather than from the panel.
type ForwardProbe struct {
	ForwardID string    `json:"forward_id"`
	ServerID  string    `json:"server_id,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	Attempts  int       `json:"attempts"`
	Successes int       `json:"successes"`
	LatencyMS float64   `json:"latency_ms"`
	JitterMS  float64   `json:"jitter_ms"`
	LossPct   float64   `json:"loss_percent"`
	Status    string    `json:"status"`
	LastError string    `json:"last_error,omitempty"`
}

func (p ForwardProbe) Validate() error {
	if strings.TrimSpace(p.ForwardID) == "" || p.Attempts < 1 || p.Attempts > 10 || p.Successes < 0 || p.Successes > p.Attempts {
		return errors.New("forward probe counts are invalid")
	}
	if p.LatencyMS < 0 || p.LatencyMS > 120000 || p.JitterMS < 0 || p.JitterMS > 120000 || p.LossPct < 0 || p.LossPct > 100 {
		return errors.New("forward probe metrics are invalid")
	}
	if !slices.Contains([]string{"stable", "degraded", "down", "unsupported"}, p.Status) {
		return errors.New("forward probe status is invalid")
	}
	if len(p.LastError) > 512 {
		return errors.New("forward probe error is too long")
	}
	return nil
}

type Node struct {
	ID         string       `json:"id"`
	ServerID   string       `json:"server_id"`
	Name       string       `json:"name"`
	Protocol   Protocol     `json:"protocol"`
	ListenPort uint16       `json:"listen_port"`
	Enabled    bool         `json:"enabled"`
	Managed    bool         `json:"managed"`
	Source     string       `json:"source,omitempty"`
	LastSeenAt time.Time    `json:"last_seen_at,omitempty"`
	Reality    *RealitySpec `json:"reality,omitempty"`
	SS         *SSSpec      `json:"shadowsocks,omitempty"`
	Snell      *SnellSpec   `json:"snell,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
}

type RealitySpec struct {
	UUID              string            `json:"uuid"`
	Flow              string            `json:"flow,omitempty"`
	HandshakeServer   string            `json:"handshake_server"`
	HandshakePort     uint16            `json:"handshake_port"`
	ServerName        string            `json:"server_name"`
	PrivateKey        string            `json:"private_key"`
	PublicKey         string            `json:"public_key"`
	ShortIDs          []string          `json:"short_ids"`
	Fingerprint       string            `json:"fingerprint"`
	Transport         string            `json:"transport"`
	TransportSettings map[string]string `json:"transport_settings,omitempty"`
	MaxTimeDifference string            `json:"max_time_difference,omitempty"`
}

type SSSpec struct {
	Method        string `json:"method"`
	Password      string `json:"password"`
	AllowInsecure bool   `json:"allow_insecure,omitempty"`
}

type SnellSpec struct {
	Version       int    `json:"version"`
	PSK           string `json:"psk"`
	ObfsMode      string `json:"obfs_mode,omitempty"`
	Mode          string `json:"mode,omitempty"`
	AllowInsecure bool   `json:"allow_insecure,omitempty"`
}

type ForwardEngine string

const (
	ForwardSingBox ForwardEngine = "sing-box"
	ForwardRealm   ForwardEngine = "realm"
)

type Forward struct {
	ID              string        `json:"id"`
	IngressServerID string        `json:"ingress_server_id"`
	Name            string        `json:"name"`
	ListenPort      uint16        `json:"listen_port"`
	Networks        []string      `json:"networks"`
	TargetHost      string        `json:"target_host"`
	TargetPort      uint16        `json:"target_port"`
	TargetServerID  string        `json:"target_server_id,omitempty"`
	TargetNodeID    string        `json:"target_node_id,omitempty"`
	Engine          ForwardEngine `json:"engine"`
	Enabled         bool          `json:"enabled"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at,omitzero"`
}

func (n Node) Validate() error {
	if name := strings.TrimSpace(n.Name); name == "" || len(name) > 96 {
		return errors.New("node name must contain 1 to 96 characters")
	}
	if n.ListenPort == 0 {
		return errors.New("listen port must be between 1 and 65535")
	}
	switch n.Protocol {
	case ProtocolReality:
		if n.Reality == nil || n.SS != nil || n.Snell != nil {
			return errors.New("reality node must contain only reality settings")
		}
		return n.Reality.Validate()
	case ProtocolShadowsocks:
		if n.SS == nil || n.Reality != nil || n.Snell != nil {
			return errors.New("shadowsocks node must contain only shadowsocks settings")
		}
		return n.SS.Validate()
	case ProtocolSnell:
		if n.Snell == nil || n.Reality != nil || n.SS != nil {
			return errors.New("snell node must contain only snell settings")
		}
		return n.Snell.Validate()
	default:
		return fmt.Errorf("unsupported protocol %q", n.Protocol)
	}
}

func (r RealitySpec) Validate() error {
	if strings.TrimSpace(r.UUID) == "" || strings.TrimSpace(r.PrivateKey) == "" || strings.TrimSpace(r.PublicKey) == "" {
		return errors.New("reality UUID and key pair are required")
	}
	if r.Flow != "" && r.Flow != "xtls-rprx-vision" {
		return fmt.Errorf("unsupported reality flow %q", r.Flow)
	}
	if !validDomain(r.HandshakeServer) || !validDomain(r.ServerName) {
		return errors.New("reality handshake server and server name must be valid domain names, not IP addresses")
	}
	if !strings.EqualFold(strings.TrimSuffix(r.HandshakeServer, "."), strings.TrimSuffix(r.ServerName, ".")) {
		return errors.New("reality handshake server and client SNI must match")
	}
	serverName := strings.ToLower(strings.TrimSuffix(r.ServerName, "."))
	if serverName == "cloudflare.com" || strings.HasSuffix(serverName, ".cloudflare.com") {
		return errors.New("Cloudflare is not accepted as a default Reality target; choose a TLS 1.3 and H2 site validated from the deployment server")
	}
	if r.HandshakePort == 0 {
		return errors.New("reality handshake port is required")
	}
	if len(r.ShortIDs) == 0 {
		return errors.New("at least one reality short ID is required")
	}
	for _, id := range r.ShortIDs {
		if !shortIDPattern.MatchString(id) {
			return fmt.Errorf("invalid reality short ID %q", id)
		}
	}
	if r.Fingerprint == "" {
		return errors.New("reality client fingerprint is required")
	}
	if !slices.Contains([]string{"tcp", "http", "ws", "grpc", "httpupgrade"}, valueOr(r.Transport, "tcp")) {
		return fmt.Errorf("unsupported reality transport %q", r.Transport)
	}
	return nil
}

func (s SSSpec) Validate() error {
	if !slices.Contains(ShadowsocksMethods, s.Method) {
		return fmt.Errorf("unsupported shadowsocks method %q", s.Method)
	}
	if s.Method == "none" && !s.AllowInsecure {
		return errors.New("shadowsocks method none requires allow_insecure")
	}
	if strings.TrimSpace(s.Password) == "" {
		return errors.New("shadowsocks password is required")
	}
	return nil
}

func (s SnellSpec) Validate() error {
	if s.Version != 5 && s.Version != 6 {
		return errors.New("snell version must be 5 or 6")
	}
	if len(s.PSK) < 12 || len(s.PSK) > 255 {
		return errors.New("snell PSK must be 12 to 255 bytes")
	}
	if s.Version == 5 && !slices.Contains([]string{"", "none", "http"}, s.ObfsMode) {
		return fmt.Errorf("unsupported snell v5 obfs mode %q", s.ObfsMode)
	}
	if s.Version == 6 {
		if !slices.Contains([]string{"", "default", "unshaped", "unsafe-raw"}, s.Mode) {
			return fmt.Errorf("unsupported snell v6 mode %q", s.Mode)
		}
		if s.Mode == "unsafe-raw" && !s.AllowInsecure {
			return errors.New("snell unsafe-raw mode requires allow_insecure")
		}
	}
	return nil
}

func (f Forward) Validate() error {
	if name := strings.TrimSpace(f.Name); name == "" || len(name) > 96 || f.ListenPort == 0 || f.TargetPort == 0 {
		return errors.New("forward name, listen port and target port are required")
	}
	target := NormalizeHost(f.TargetHost)
	if len(target) > 253 || (net.ParseIP(target) == nil && !hostPattern.MatchString(target)) {
		return errors.New("forward target host is invalid")
	}
	if f.TargetServerID != "" && f.TargetServerID == f.IngressServerID && f.TargetPort == f.ListenPort {
		return errors.New("forward cannot target its own listening port")
	}
	if f.Engine != ForwardSingBox && f.Engine != ForwardRealm {
		return fmt.Errorf("unsupported forward engine %q", f.Engine)
	}
	if len(f.Networks) == 0 {
		return errors.New("at least one forward network is required")
	}
	for _, network := range f.Networks {
		if network != "tcp" && network != "udp" {
			return fmt.Errorf("unsupported forward network %q", network)
		}
	}
	return nil
}

// NormalizeHost keeps hostnames unchanged and stores IP literals without URL
// brackets. Callers that need host:port syntax should add brackets with
// net.JoinHostPort so IPv4 and IPv6 share one canonical representation.
func NormalizeHost(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = value[1 : len(value)-1]
	}
	if ip := net.ParseIP(value); ip != nil {
		return ip.String()
	}
	return value
}

func validDomain(value string) bool {
	value = strings.TrimSuffix(strings.TrimSpace(value), ".")
	return value != "" && net.ParseIP(value) == nil && hostPattern.MatchString(value) && strings.Contains(value, ".")
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
