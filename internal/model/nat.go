package model

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
)

// PortMapping is one range of the port forwarding a provider sets up for a
// server that shares its public IPv4 address: Count consecutive public ports
// starting at Public reach the host ports starting at Host.
type PortMapping struct {
	Public uint16 `json:"public"`
	Host   uint16 `json:"host"`
	Count  uint16 `json:"count"`
}

const maxPortMappings = 32

func (m PortMapping) hostLast() int   { return int(m.Host) + int(m.Count) - 1 }
func (m PortMapping) publicLast() int { return int(m.Public) + int(m.Count) - 1 }

func (m PortMapping) coversHost(port uint16) bool {
	return m.Count > 0 && port >= m.Host && int(port) <= m.hostLast()
}

func (m PortMapping) coversPublic(port uint16) bool {
	return m.Count > 0 && port >= m.Public && int(port) <= m.publicLast()
}

func validatePortMappings(mappings []PortMapping) error {
	if len(mappings) > maxPortMappings {
		return fmt.Errorf("a server accepts at most %d NAT port ranges", maxPortMappings)
	}
	for index, mapping := range mappings {
		if mapping.Count == 0 || mapping.Public == 0 || mapping.Host == 0 || mapping.publicLast() > 65535 || mapping.hostLast() > 65535 {
			return errors.New("NAT port ranges must lie between ports 1 and 65535")
		}
		for _, other := range mappings[:index] {
			if int(mapping.Host) <= other.hostLast() && int(other.Host) <= mapping.hostLast() {
				return errors.New("NAT port ranges must not overlap on the host")
			}
			if int(mapping.Public) <= other.publicLast() && int(other.Public) <= mapping.publicLast() {
				return errors.New("NAT port ranges must not overlap on the public address")
			}
		}
	}
	return nil
}

// SortPortMappings orders mappings by host port, the order ports are
// allocated in.
func SortPortMappings(mappings []PortMapping) {
	slices.SortFunc(mappings, func(left, right PortMapping) int { return int(left.Host) - int(right.Host) })
}

// NAT reports whether only the host ports in NATPorts are reachable over the
// server's public IPv4 address.
func (s Server) NAT() bool { return len(s.NATPorts) > 0 }

// MapsHostPort reports whether clients can reach a host port over IPv4.
func (s Server) MapsHostPort(port uint16) bool {
	return !s.NAT() || slices.ContainsFunc(s.NATPorts, func(mapping PortMapping) bool { return mapping.coversHost(port) })
}

// PublicPort returns the port clients dial at address to reach a host port.
// NAT port forwarding covers IPv4 only: an IPv6 address reaches the host port
// directly, and a hostname is taken to resolve to the shared IPv4 address.
func (s Server) PublicPort(address string, port uint16) (uint16, bool) {
	if !s.NAT() || isIPv6Literal(address) {
		return port, true
	}
	for _, mapping := range s.NATPorts {
		if mapping.coversHost(port) {
			return mapping.Public + (port - mapping.Host), true
		}
	}
	return 0, false
}

// HostPort returns the host port that a public port at address reaches.
func (s Server) HostPort(address string, port uint16) (uint16, bool) {
	if !s.NAT() || isIPv6Literal(address) {
		return port, true
	}
	for _, mapping := range s.NATPorts {
		if mapping.coversPublic(port) {
			return mapping.Host + (port - mapping.Public), true
		}
	}
	return 0, false
}

// HostPortRanges describes the mapped host ports, such as "10001-10020、10022".
func (s Server) HostPortRanges() string {
	ranges := make([]string, 0, len(s.NATPorts))
	for _, mapping := range s.NATPorts {
		if mapping.Count == 1 {
			ranges = append(ranges, strconv.Itoa(int(mapping.Host)))
		} else {
			ranges = append(ranges, fmt.Sprintf("%d-%d", mapping.Host, mapping.hostLast()))
		}
	}
	return strings.Join(ranges, "、")
}

func isIPv6Literal(address string) bool {
	ip := net.ParseIP(NormalizeHost(address))
	return ip != nil && ip.To4() == nil
}
