package api

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// DefaultTrustedProxies trusts a reverse proxy on the same host.
var DefaultTrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}

// CloudflareTrustedProxies lists Cloudflare's published edge IPv4 and IPv6
// ranges (https://www.cloudflare.com/ips-v4 and ips-v6).
var CloudflareTrustedProxies = []netip.Prefix{
	netip.MustParsePrefix("173.245.48.0/20"),
	netip.MustParsePrefix("103.21.244.0/22"),
	netip.MustParsePrefix("103.22.200.0/22"),
	netip.MustParsePrefix("103.31.4.0/22"),
	netip.MustParsePrefix("141.101.64.0/18"),
	netip.MustParsePrefix("108.162.192.0/18"),
	netip.MustParsePrefix("190.93.240.0/20"),
	netip.MustParsePrefix("188.114.96.0/20"),
	netip.MustParsePrefix("197.234.240.0/22"),
	netip.MustParsePrefix("198.41.128.0/17"),
	netip.MustParsePrefix("162.158.0.0/15"),
	netip.MustParsePrefix("104.16.0.0/13"),
	netip.MustParsePrefix("104.24.0.0/14"),
	netip.MustParsePrefix("172.64.0.0/13"),
	netip.MustParsePrefix("131.0.72.0/22"),
	netip.MustParsePrefix("2400:cb00::/32"),
	netip.MustParsePrefix("2606:4700::/32"),
	netip.MustParsePrefix("2803:f800::/32"),
	netip.MustParsePrefix("2405:b500::/32"),
	netip.MustParsePrefix("2405:8100::/32"),
	netip.MustParsePrefix("2a06:98c0::/29"),
	netip.MustParsePrefix("2c0f:f248::/32"),
}

// ParseTrustedProxies reads a comma- or space-separated list of addresses, CIDR
// prefixes, and preset names ("cloudflare"), returning them after
// DefaultTrustedProxies so a proxy on the same host stays trusted.
func ParseTrustedProxies(value string) ([]netip.Prefix, error) {
	fields := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
	prefixes := append(make([]netip.Prefix, 0, len(DefaultTrustedProxies)+len(fields)), DefaultTrustedProxies...)
	for _, field := range fields {
		if strings.EqualFold(field, "cloudflare") {
			prefixes = append(prefixes, CloudflareTrustedProxies...)
			continue
		}
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
	var outermost netip.Addr
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		outermost = hop.Unmap()
		if !s.trustedProxy(hop) {
			return outermost.String()
		}
	}
	if outermost.IsValid() {
		return outermost.String()
	}
	return direct
}

func (s *Server) trustedProxy(address netip.Addr) bool {
	return prefixListContains(s.trustedProxies, address)
}

func isCloudflareProxy(address netip.Addr) bool {
	return prefixListContains(CloudflareTrustedProxies, address)
}

func prefixListContains(prefixes []netip.Prefix, address netip.Addr) bool {
	address = address.Unmap()
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
