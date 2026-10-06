// Package discovery reads existing proxy configuration without modifying it.
// The resulting nodes are reported to the control plane as external, read-only
// resources; Portolan never includes them in its desired-state writes.
package discovery

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KurisuT7/portolan/internal/model"
)

const (
	maxConfigBytes = 4 << 20
	maxNodes       = 100
)

var versionPattern = regexp.MustCompile(`(?i)\bv([56])(?:\.|\b)`)

type Options struct {
	Paths         []string
	SnellBinaries []string
	SnellVersion  int
}

type Report struct {
	Items    []model.Node
	Warnings []string
}

func ScanSystem(ctx context.Context) Report {
	return Scan(ctx, Options{
		Paths: []string{
			"/etc/sing-box/config.json",
			"/etc/sing-box/conf/*.json",
			"/etc/sing-box/conf.d/*.json",
			"/usr/local/etc/sing-box/config.json",
			"/usr/local/etc/sing-box/conf.d/*.json",
			"/etc/s-box/sb.json",
			"/etc/snell/snell-server.conf",
			"/etc/snell/snell.conf",
			"/etc/snell-server.conf",
		},
		SnellBinaries: []string{
			"/usr/local/bin/snell-server",
			"/usr/bin/snell-server",
			"/usr/local/snell/snell-server",
			"/opt/snell/snell-server",
		},
	})
}

func Scan(ctx context.Context, options Options) Report {
	report := Report{Items: []model.Node{}, Warnings: []string{}}
	paths := expandPaths(options.Paths)
	snellVersion := options.SnellVersion
	if snellVersion == 0 {
		snellVersion = detectSnellVersion(ctx, options.SnellBinaries)
	}
	seen := map[string]struct{}{}
	for _, path := range paths {
		if len(report.Items) >= maxNodes || isPortolanRuntime(path) {
			continue
		}
		data, err := readRegularFile(path)
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		var nodes []model.Node
		if strings.HasSuffix(strings.ToLower(path), ".json") {
			nodes, err = parseSingBox(path, data)
		} else {
			nodes, err = parseSnell(path, data, snellVersion)
		}
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		for _, node := range nodes {
			key := fmt.Sprintf("%s:%d", node.Protocol, node.ListenPort)
			if _, exists := seen[key]; exists {
				continue
			}
			if err := node.Validate(); err != nil {
				report.Warnings = append(report.Warnings, fmt.Sprintf("%s port %d: %v", path, node.ListenPort, err))
				continue
			}
			seen[key] = struct{}{}
			report.Items = append(report.Items, node)
			if len(report.Items) >= maxNodes {
				break
			}
		}
	}
	return report
}

type singBoxConfig struct {
	Inbounds  []singBoxInbound `json:"inbounds"`
	Outbounds []struct {
		Tag string `json:"tag"`
	} `json:"outbounds"`
}

type singBoxInbound struct {
	Type       string `json:"type"`
	Tag        string `json:"tag"`
	ListenPort uint16 `json:"listen_port"`
	Method     string `json:"method"`
	Password   string `json:"password"`
	Version    int    `json:"version"`
	PSK        string `json:"psk"`
	ObfsMode   string `json:"obfs_mode"`
	Mode       string `json:"mode"`
	Users      []struct {
		Name     string `json:"name"`
		UUID     string `json:"uuid"`
		Flow     string `json:"flow"`
		Password string `json:"password"`
	} `json:"users"`
	TLS *struct {
		Enabled    bool   `json:"enabled"`
		ServerName string `json:"server_name"`
		Reality    *struct {
			Enabled   bool `json:"enabled"`
			Handshake struct {
				Server     string `json:"server"`
				ServerPort uint16 `json:"server_port"`
			} `json:"handshake"`
			PrivateKey        string   `json:"private_key"`
			PublicKey         string   `json:"public_key"`
			ShortIDs          []string `json:"short_id"`
			MaxTimeDifference string   `json:"max_time_difference"`
		} `json:"reality"`
	} `json:"tls"`
	Transport *struct {
		Type        string          `json:"type"`
		Path        string          `json:"path"`
		ServiceName string          `json:"service_name"`
		Host        json.RawMessage `json:"host"`
		Headers     struct {
			Host json.RawMessage `json:"host"`
		} `json:"headers"`
	} `json:"transport"`
}

func parseSingBox(path string, data []byte) ([]model.Node, error) {
	var config singBoxConfig
	if err := json.Unmarshal(stripJSONComments(data), &config); err != nil {
		return nil, fmt.Errorf("invalid sing-box JSON: %w", err)
	}
	publicKey := ""
	for _, outbound := range config.Outbounds {
		if strings.HasPrefix(outbound.Tag, "public_key_") {
			publicKey = strings.TrimPrefix(outbound.Tag, "public_key_")
			break
		}
	}
	nodes := make([]model.Node, 0, len(config.Inbounds))
	for index, inbound := range config.Inbounds {
		if inbound.ListenPort == 0 {
			continue
		}
		base := model.Node{
			Name:       discoveredName(inbound.Tag, path, inbound.Type, index),
			ListenPort: inbound.ListenPort,
			Enabled:    true,
			Managed:    false,
			Source:     "sing-box:" + filepath.Clean(path),
		}
		switch inbound.Type {
		case "vless":
			if inbound.TLS == nil || !inbound.TLS.Enabled || inbound.TLS.Reality == nil || !inbound.TLS.Reality.Enabled || len(inbound.Users) == 0 {
				continue
			}
			reality := inbound.TLS.Reality
			key := firstNonEmpty(reality.PublicKey, publicKey)
			if key == "" {
				key, _ = realityPublicKey(reality.PrivateKey)
			}
			handshakePort := reality.Handshake.ServerPort
			if handshakePort == 0 {
				handshakePort = 443
			}
			serverName := firstNonEmpty(inbound.TLS.ServerName, reality.Handshake.Server)
			transport, settings := transportSettings(inbound.Transport)
			base.Protocol = model.ProtocolReality
			base.Reality = &model.RealitySpec{
				UUID: inbound.Users[0].UUID, Flow: inbound.Users[0].Flow,
				HandshakeServer: reality.Handshake.Server, HandshakePort: handshakePort,
				ServerName: serverName, PrivateKey: reality.PrivateKey, PublicKey: key,
				ShortIDs: reality.ShortIDs, Fingerprint: "chrome", Transport: transport,
				TransportSettings: settings, MaxTimeDifference: reality.MaxTimeDifference,
			}
		case "shadowsocks":
			password := inbound.Password
			if len(inbound.Users) > 0 && inbound.Users[0].Password != "" {
				if strings.HasPrefix(inbound.Method, "2022-") && password != "" {
					password += ":" + inbound.Users[0].Password
				} else {
					password = inbound.Users[0].Password
				}
				if inbound.Users[0].Name != "" {
					base.Name = trimName(base.Name + " · " + inbound.Users[0].Name)
				}
			}
			base.Protocol = model.ProtocolShadowsocks
			base.SS = &model.SSSpec{Method: inbound.Method, Password: password, AllowInsecure: inbound.Method == "none"}
		case "snell":
			base.Protocol = model.ProtocolSnell
			base.Snell = &model.SnellSpec{
				Version: inbound.Version, PSK: inbound.PSK, ObfsMode: inbound.ObfsMode,
				Mode: inbound.Mode, AllowInsecure: inbound.Mode == "unsafe-raw",
			}
		default:
			continue
		}
		nodes = append(nodes, base)
	}
	return nodes, nil
}

func parseSnell(path string, data []byte, detectedVersion int) ([]model.Node, error) {
	values := map[string]string{}
	section := ""
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(strings.Trim(line, "[]")))
			continue
		}
		if section != "" && section != "snell-server" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
		}
	}
	port, err := parseListenPort(values["listen"])
	if err != nil {
		return nil, err
	}
	version := detectedVersion
	if configured, parseErr := strconv.Atoi(values["version"]); parseErr == nil && (configured == 5 || configured == 6) {
		version = configured
	}
	if version != 5 && version != 6 {
		return nil, errors.New("Snell v5/v6 binary version could not be identified")
	}
	psk := values["psk"]
	if psk == "" {
		return nil, errors.New("Snell PSK is missing")
	}
	node := model.Node{
		Name: trimName(fmt.Sprintf("已发现 Snell v%d · %d", version, port)), Protocol: model.ProtocolSnell,
		ListenPort: port, Enabled: true, Managed: false, Source: "snell:" + filepath.Clean(path),
		Snell: &model.SnellSpec{Version: version, PSK: psk},
	}
	if version == 5 {
		node.Snell.ObfsMode = strings.ToLower(values["obfs"])
		if node.Snell.ObfsMode == "tls" {
			// sing-box v5 exposes none/http; standalone Snell's historical tls
			// obfuscation is not client-export compatible here, so keep it visible
			// only when it maps to a supported mode.
			node.Snell.ObfsMode = ""
		}
	} else {
		node.Snell.Mode = strings.ToLower(values["mode"])
		node.Snell.AllowInsecure = node.Snell.Mode == "unsafe-raw"
	}
	return []model.Node{node}, nil
}

func transportSettings(transport *struct {
	Type        string          `json:"type"`
	Path        string          `json:"path"`
	ServiceName string          `json:"service_name"`
	Host        json.RawMessage `json:"host"`
	Headers     struct {
		Host json.RawMessage `json:"host"`
	} `json:"headers"`
}) (string, map[string]string) {
	if transport == nil || transport.Type == "" {
		return "tcp", nil
	}
	settings := map[string]string{}
	if transport.Path != "" {
		settings["path"] = transport.Path
	}
	if transport.ServiceName != "" {
		settings["service_name"] = transport.ServiceName
	}
	host := rawString(transport.Host)
	if host == "" {
		host = rawString(transport.Headers.Host)
	}
	if host != "" {
		settings["host"] = host
	}
	return transport.Type, settings
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	var values []string
	if json.Unmarshal(raw, &values) == nil && len(values) > 0 {
		return values[0]
	}
	return ""
}

func realityPublicKey(privateKey string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(privateKey))
	if err != nil {
		return "", err
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

func detectSnellVersion(ctx context.Context, binaries []string) int {
	for _, binary := range binaries {
		info, err := os.Stat(binary)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
			continue
		}
		commandCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		output, _ := exec.CommandContext(commandCtx, binary, "--version").CombinedOutput()
		cancel()
		match := versionPattern.FindStringSubmatch(string(output))
		if len(match) == 2 {
			version, _ := strconv.Atoi(match[1])
			return version
		}
	}
	return 0
}

func expandPaths(patterns []string) []string {
	unique := map[string]struct{}{}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, path := range matches {
			unique[filepath.Clean(path)] = struct{}{}
		}
	}
	paths := make([]string, 0, len(unique))
	for path := range unique {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func readRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("not a regular configuration file")
	}
	if info.Size() > maxConfigBytes {
		return nil, errors.New("configuration file is too large")
	}
	return os.ReadFile(path)
}

func parseListenPort(listen string) (uint16, error) {
	listen = strings.TrimSpace(listen)
	index := strings.LastIndex(listen, ":")
	if index < 0 || index == len(listen)-1 {
		return 0, errors.New("Snell listen address is invalid")
	}
	port, err := strconv.ParseUint(strings.TrimSpace(listen[index+1:]), 10, 16)
	if err != nil || port == 0 {
		return 0, errors.New("Snell listen port is invalid")
	}
	return uint16(port), nil
}

func discoveredName(tag, path, protocol string, index int) string {
	if strings.TrimSpace(tag) != "" {
		return trimName("已发现 · " + strings.TrimSpace(tag))
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if name == "config" || name == "" {
		name = protocol + " " + strconv.Itoa(index+1)
	}
	return trimName("已发现 · " + name)
}

func trimName(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= 96 {
		return value
	}
	return string(runes[:96])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func isPortolanRuntime(path string) bool {
	clean := filepath.Clean(path)
	runtime := filepath.Clean("/etc/portolan/runtime")
	return clean == runtime || strings.HasPrefix(clean, runtime+string(os.PathSeparator))
}

func stripJSONComments(data []byte) []byte {
	result := make([]byte, 0, len(data))
	inString := false
	escaped := false
	for index := 0; index < len(data); index++ {
		char := data[index]
		if inString {
			result = append(result, char)
			if escaped {
				escaped = false
			} else if char == '\\' {
				escaped = true
			} else if char == '"' {
				inString = false
			}
			continue
		}
		if char == '"' {
			inString = true
			result = append(result, char)
			continue
		}
		if char == '/' && index+1 < len(data) && data[index+1] == '/' {
			for index < len(data) && data[index] != '\n' {
				index++
			}
			if index < len(data) {
				result = append(result, '\n')
			}
			continue
		}
		result = append(result, char)
	}
	return result
}
