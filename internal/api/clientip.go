package api

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// DefaultTrustedProxies trusts only a reverse proxy on the same host.
var DefaultTrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}

// ParseTrustedProxies reads a comma- or space-separated list of addresses and
// CIDR prefixes. An empty value returns DefaultTrustedProxies.
func ParseTrustedProxies(value string) ([]netip.Prefix, error) {
	fields := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
	if len(fields) == 0 {
		return DefaultTrustedProxies, nil
	}
	prefixes := make([]netip.Prefix, 0, len(fields))
	for _, field := range fields {
		if strings.Contains(field, "/") {
			prefix, err := netip.ParsePrefix(field)
			if err != nil {
				return nil, fmt.Errorf("invalid trusted proxy %q: %w", field, err)
			}
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		address, err := netip.ParseAddr(field)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy %q: %w", field, err)
		}
		prefixes = append(prefixes, netip.PrefixFrom(address.Unmap(), address.Unmap().BitLen()))
	}
	return prefixes, nil
}

// clientIP returns the address of the client that sent the request. When the
// direct peer is a trusted proxy it walks X-Forwarded-For from the right and
// returns the first address that is not a trusted proxy, so a client cannot
// choose its address by sending its own header.
func (s *Server) clientIP(r *http.Request) string {
	direct := remoteIP(r)
	address, err := netip.ParseAddr(direct)
	if err != nil || !s.trustedProxy(address) {
		return direct
	}
	var hops []string
	for _, header := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(header, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		if !s.trustedProxy(hop) {
			return hop.Unmap().String()
		}
	}
	return direct
}

func (s *Server) trustedProxy(address netip.Addr) bool {
	address = address.Unmap()
	for _, prefix := range s.trustedProxies {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
