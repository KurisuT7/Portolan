package configgen

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/KurisuT7/Portolan/internal/model"
)

type fragment struct {
	Inbounds []map[string]any `json:"inbounds"`
}

func RealmConfig(forward model.Forward) ([]byte, error) {
	if err := forward.Validate(); err != nil {
		return nil, err
	}
	if forward.Engine != model.ForwardRealm {
		return nil, fmt.Errorf("forward %s is assigned to sing-box, not realm", forward.ID)
	}
	useTCP, useUDP := false, false
	for _, network := range forward.Networks {
		useTCP = useTCP || network == "tcp"
		useUDP = useUDP || network == "udp"
	}
	listen := net.JoinHostPort("::", strconv.Itoa(int(forward.ListenPort)))
	remote := net.JoinHostPort(model.NormalizeHost(forward.TargetHost), strconv.Itoa(int(forward.TargetPort)))
	value := "[log]\nlevel = \"warn\"\n\n[network]\nno_tcp = " + strconv.FormatBool(!useTCP) +
		"\nuse_udp = " + strconv.FormatBool(useUDP) + "\nipv6_only = false\n\n[[endpoints]]\nlisten = " + strconv.Quote(listen) +
		"\nremote = " + strconv.Quote(remote) + "\n"
	return []byte(value), nil
}

func BaseConfig() ([]byte, error) {
	value := map[string]any{
		// Info level records the destination of every proxied connection.
		"log":       map[string]any{"level": "warn", "timestamp": true},
		"outbounds": []map[string]any{{"type": "direct", "tag": "direct"}},
	}
	return json.MarshalIndent(value, "", "  ")
}

func NodeFragment(node model.Node) ([]byte, error) {
	if err := node.Validate(); err != nil {
		return nil, err
	}
	inbound := map[string]any{
		"tag":         "node-" + node.ID,
		"listen":      "::",
		"listen_port": node.ListenPort,
	}
	switch node.Protocol {
	case model.ProtocolReality:
		inbound["type"] = "vless"
		user := map[string]any{"name": node.Name, "uuid": node.Reality.UUID}
		if node.Reality.Flow != "" {
			user["flow"] = node.Reality.Flow
		}
		inbound["users"] = []map[string]any{user}
		reality := map[string]any{
			"enabled":     true,
			"private_key": node.Reality.PrivateKey,
			"short_id":    node.Reality.ShortIDs,
			"handshake": map[string]any{
				"server":      node.Reality.HandshakeServer,
				"server_port": node.Reality.HandshakePort,
			},
		}
		if node.Reality.MaxTimeDifference != "" {
			reality["max_time_difference"] = node.Reality.MaxTimeDifference
		}
		inbound["tls"] = map[string]any{
			"enabled":     true,
			"server_name": node.Reality.ServerName,
			"reality":     reality,
		}
		if transport := realityTransport(*node.Reality); transport != nil {
			inbound["transport"] = transport
		}
	case model.ProtocolShadowsocks:
		inbound["type"] = "shadowsocks"
		inbound["method"] = node.SS.Method
		inbound["password"] = node.SS.Password
	case model.ProtocolSnell:
		inbound["type"] = "snell"
		inbound["version"] = node.Snell.Version
		inbound["psk"] = node.Snell.PSK
		if node.Snell.Version == 5 && node.Snell.ObfsMode != "" {
			inbound["obfs_mode"] = node.Snell.ObfsMode
		}
		if node.Snell.Version == 6 && node.Snell.Mode != "" {
			inbound["mode"] = node.Snell.Mode
		}
	}
	return json.MarshalIndent(fragment{Inbounds: []map[string]any{inbound}}, "", "  ")
}

func ForwardFragment(forward model.Forward) ([]byte, error) {
	if err := forward.Validate(); err != nil {
		return nil, err
	}
	if forward.Engine != model.ForwardSingBox {
		return nil, fmt.Errorf("forward %s is assigned to realm, not sing-box", forward.ID)
	}
	inbound := map[string]any{
		"type":             "direct",
		"tag":              "forward-" + forward.ID,
		"listen":           "::",
		"listen_port":      forward.ListenPort,
		"network":          forward.Networks,
		"override_address": model.NormalizeHost(forward.TargetHost),
		"override_port":    forward.TargetPort,
	}
	return json.MarshalIndent(fragment{Inbounds: []map[string]any{inbound}}, "", "  ")
}

func realityTransport(spec model.RealitySpec) map[string]any {
	transportType := spec.Transport
	if transportType == "" || transportType == "tcp" {
		return nil
	}
	transport := map[string]any{"type": transportType}
	for key, value := range spec.TransportSettings {
		switch key {
		case "path", "service_name":
			transport[key] = value
		case "host":
			// The inbound HTTP transport can restrict Host values. WebSocket and
			// HTTPUpgrade use Host only on the client side and reject it here.
			if transportType != "http" {
				continue
			}
			if strings.Contains(value, ",") {
				transport[key] = strings.Split(value, ",")
			} else {
				transport[key] = []string{value}
			}
		}
	}
	return transport
}
