// Package traffic reads the byte counters an Agent reports: the network
// interfaces that carry the host's default routes, read from /proc, and the
// listening ports Portolan knows on the host, counted by a dedicated nftables
// table (see Accounting).
package traffic

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/KurisuT7/Portolan/internal/model"
)

const (
	routeUp     = 0x0001
	routeReject = 0x0200
	// maxInterfaces matches the limit the panel accepts in one report.
	maxInterfaces = 32
)

// Interfaces that never carry a host's own public traffic. They are skipped
// only when no default route names the public interfaces.
var virtualPrefixes = []string{"lo", "docker", "veth", "br-", "virbr", "vnet", "cni", "flannel", "cali", "kube", "tun", "tap", "wg", "tailscale", "zt", "dummy", "ifb"}

// Collector assembles the report an Agent sends to the panel.
type Collector struct {
	ProcRoot   string
	Accounting *Accounting
}

// Report reads every counter that is available. Missing interface counters
// leave the list empty; missing port counters are explained by PortError.
func (c *Collector) Report(ctx context.Context, ports []uint16) model.TrafficReport {
	procRoot := c.ProcRoot
	if procRoot == "" {
		procRoot = "/proc"
	}
	report := model.TrafficReport{BootID: BootID(procRoot), Interfaces: []model.InterfaceTraffic{}, Ports: []model.PortTraffic{}}
	if interfaces, err := ReadInterfaces(procRoot); err != nil {
		slog.Debug("interface traffic counters are unavailable", "error", err)
	} else {
		report.Interfaces = interfaces
	}
	epoch, counters, err := c.Accounting.Counters(ctx, report.BootID, ports)
	switch {
	case errors.Is(err, ErrNFTMissing):
		report.PortError = model.TrafficPortsUnavailable
	case err != nil:
		slog.Warn("port traffic accounting failed", "error", err)
		report.PortError = model.TrafficPortsFailed
	case len(counters) > 0:
		report.PortEpoch = epoch
		report.Ports = counters
	}
	return report
}

// BootID identifies the current boot; interface counters restart with it.
func BootID(procRoot string) string {
	data, err := os.ReadFile(filepath.Join(procRoot, "sys", "kernel", "random", "boot_id"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ReadInterfaces returns the counters of the interfaces that hold a default
// route. When the main routing table has no default route (policy routing,
// some containers), it falls back to every interface that is not virtual.
func ReadInterfaces(procRoot string) ([]model.InterfaceTraffic, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, "net", "dev"))
	if err != nil {
		return nil, err
	}
	routes := defaultRouteInterfaces(procRoot)
	result := []model.InterfaceTraffic{}
	for _, item := range parseNetDev(string(data)) {
		if len(routes) > 0 && !routes[item.Name] {
			continue
		}
		if len(routes) == 0 && slices.ContainsFunc(virtualPrefixes, func(prefix string) bool { return strings.HasPrefix(item.Name, prefix) }) {
			continue
		}
		result = append(result, item)
	}
	slices.SortFunc(result, func(a, b model.InterfaceTraffic) int { return strings.Compare(a.Name, b.Name) })
	if len(result) > maxInterfaces {
		result = result[:maxInterfaces]
	}
	return result, nil
}

// parseNetDev reads /proc/net/dev: after the interface name come eight receive
// fields, the first of which is bytes, and then the transmit fields.
func parseNetDev(text string) []model.InterfaceTraffic {
	var items []model.InterfaceTraffic
	for _, line := range strings.Split(text, "\n") {
		name, values, found := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		fields := strings.Fields(values)
		if !found || name == "" || len(fields) < 9 {
			continue
		}
		received, receiveErr := strconv.ParseUint(fields[0], 10, 64)
		sent, sendErr := strconv.ParseUint(fields[8], 10, 64)
		if receiveErr != nil || sendErr != nil {
			continue
		}
		items = append(items, model.InterfaceTraffic{Name: name, RX: received, TX: sent})
	}
	return items
}

// defaultRouteInterfaces names the interfaces of the usable IPv4 and IPv6
// default routes in the main routing table.
func defaultRouteInterfaces(procRoot string) map[string]bool {
	names := map[string]bool{}
	if data, err := os.ReadFile(filepath.Join(procRoot, "net", "route")); err == nil {
		// Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 8 || fields[1] != "00000000" || fields[7] != "00000000" {
				continue
			}
			if usableRoute(fields[3]) {
				names[fields[0]] = true
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(procRoot, "net", "ipv6_route")); err == nil {
		// Destination Prefix Source Prefix NextHop Metric RefCnt Use Flags Iface
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 10 || fields[1] != "00" || strings.Trim(fields[0], "0") != "" {
				continue
			}
			if usableRoute(fields[8]) {
				names[fields[9]] = true
			}
		}
	}
	// The kernel lists unreachable defaults on the loopback interface.
	delete(names, "lo")
	return names
}

func usableRoute(hexFlags string) bool {
	flags, err := strconv.ParseUint(hexFlags, 16, 32)
	return err == nil && flags&routeUp != 0 && flags&routeReject == 0
}
