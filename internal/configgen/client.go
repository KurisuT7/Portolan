package configgen

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/KurisuT7/portolan/internal/model"
)

type PublishedEndpoint struct {
	Address string `json:"address"`
	Port    uint16 `json:"port"`
	Name    string `json:"name"`
}

type ClientExport struct {
	URI       string         `json:"uri,omitempty"`
	SurgeLine string         `json:"surge_line,omitempty"`
	Details   map[string]any `json:"details"`
}

func ExportClient(node model.Node, endpoint PublishedEndpoint) (ClientExport, error) {
	if err := node.Validate(); err != nil {
		return ClientExport{}, err
	}
	hostPort := net.JoinHostPort(endpoint.Address, strconv.Itoa(int(endpoint.Port)))
	switch node.Protocol {
	case model.ProtocolReality:
		query := url.Values{
			"encryption": {"none"},
			"security":   {"reality"},
			"sni":        {node.Reality.ServerName},
			"fp":         {node.Reality.Fingerprint},
			"pbk":        {node.Reality.PublicKey},
			"sid":        {node.Reality.ShortIDs[0]},
			"type":       {valueOr(node.Reality.Transport, "tcp")},
		}
		if node.Reality.Flow != "" {
			query.Set("flow", node.Reality.Flow)
		}
		for key, value := range node.Reality.TransportSettings {
			// Share links use camelCase where sing-box configuration uses snake_case.
			if key == "service_name" {
				key = "serviceName"
			}
			query.Set(key, value)
		}
		uri := &url.URL{Scheme: "vless", User: url.User(node.Reality.UUID), Host: hostPort, RawQuery: query.Encode(), Fragment: endpoint.Name}
		return ClientExport{URI: uri.String(), Details: map[string]any{"protocol": "vless", "reality": true}}, nil
	case model.ProtocolShadowsocks:
		userinfo := base64.RawURLEncoding.EncodeToString([]byte(node.SS.Method + ":" + node.SS.Password))
		// SIP002 forbids Base64 user info for AEAD-2022 methods.
		if strings.HasPrefix(node.SS.Method, "2022-") {
			userinfo = percentEncode(node.SS.Method) + ":" + percentEncode(node.SS.Password)
		}
		return ClientExport{
			URI:     "ss://" + userinfo + "@" + hostPort + "#" + url.PathEscape(endpoint.Name),
			Details: map[string]any{"protocol": "shadowsocks", "method": node.SS.Method},
		}, nil
	case model.ProtocolSnell:
		line := fmt.Sprintf("%s = snell, %s, %d, psk=%s, version=%d", endpoint.Name, endpoint.Address, endpoint.Port, node.Snell.PSK, node.Snell.Version)
		details := map[string]any{"protocol": "snell", "version": node.Snell.Version}
		if node.Snell.Version == 5 && node.Snell.ObfsMode == "http" {
			line += ", obfs=http"
			details["obfs"] = "http"
		}
		if node.Snell.Version == 6 && node.Snell.Mode != "" && node.Snell.Mode != "default" {
			line += ", mode=" + node.Snell.Mode
			details["mode"] = node.Snell.Mode
		}
		return ClientExport{SurgeLine: line, Details: details}, nil
	}
	return ClientExport{}, fmt.Errorf("unsupported protocol %q", node.Protocol)
}

// percentEncode escapes every byte outside the RFC 3986 unreserved set.
func percentEncode(value string) string {
	var encoded strings.Builder
	for index := 0; index < len(value); index++ {
		c := value[index]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			encoded.WriteByte(c)
		} else {
			fmt.Fprintf(&encoded, "%%%02X", c)
		}
	}
	return encoded.String()
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
