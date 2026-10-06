package traffic

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/KurisuT7/Portolan/internal/model"
)

const netDev = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:  912345    1200    0    0    0     0          0         0   912345    1200    0    0    0     0       0          0
  eth0:9876543210 7654321    0    0    0     0          0         0 1234567890 3456789    0    0    0     0       0          0
  eth1: 5000      50    0    0    0     0          0         0     6000      60    0    0    0     0       0          0
docker0:  700       7    0    0    0     0          0         0      800       8    0    0    0     0       0          0
  wg0: 4000      40    0    0    0     0          0         0     3000      30    0    0    0     0       0          0
`

// eth0 holds the IPv4 default route (via 192.0.2.1); wg0 holds an IPv6 default
// route; eth1 only reaches 198.51.100.0/24; the loopback unreachable default
// is ignored.
const ipv4Routes = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
eth0	00000000	010200C0	0003	0	0	100	00000000	0	0	0
eth1	006433C6	00000000	0001	0	0	0	00FFFFFF	0	0	0
`

const ipv6Routes = `20010db8000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000001 00000000 00000001 eth0
00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000400 00000001 00000000 00000001 wg0
00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200 lo
`

func procFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestInterfacesFollowDefaultRoutes(t *testing.T) {
	t.Parallel()
	root := procFixture(t, map[string]string{"net/dev": netDev, "net/route": ipv4Routes, "net/ipv6_route": ipv6Routes})
	got, err := ReadInterfaces(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.InterfaceTraffic{{Name: "eth0", RX: 9876543210, TX: 1234567890}, {Name: "wg0", RX: 4000, TX: 3000}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("interfaces = %#v", got)
	}
}

func TestInterfacesWithoutDefaultRouteSkipVirtualDevices(t *testing.T) {
	t.Parallel()
	root := procFixture(t, map[string]string{"net/dev": netDev, "net/route": strings.SplitN(ipv4Routes, "\n", 2)[0] + "\n"})
	got, err := ReadInterfaces(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.InterfaceTraffic{{Name: "eth0", RX: 9876543210, TX: 1234567890}, {Name: "eth1", RX: 5000, TX: 6000}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("interfaces = %#v", got)
	}
}

func TestBootIDIsTrimmed(t *testing.T) {
	t.Parallel()
	root := procFixture(t, map[string]string{"sys/kernel/random/boot_id": "0d5c8a9e-6b1f-4f3e-9a51-2c7e8d4b1f60\n"})
	if got := BootID(root); got != "0d5c8a9e-6b1f-4f3e-9a51-2c7e8d4b1f60" {
		t.Fatalf("boot ID = %q", got)
	}
	if got := BootID(t.TempDir()); got != "" {
		t.Fatalf("missing boot ID = %q", got)
	}
}

func TestScriptKeepsNamedCountersAndReplacesRules(t *testing.T) {
	t.Parallel()
	got := Script([]uint16{443, 20001})
	want := `add table inet portolan
add chain inet portolan input { type filter hook input priority 10 ; policy accept ; }
add chain inet portolan output { type filter hook output priority 10 ; policy accept ; }
add counter inet portolan rx_443
add counter inet portolan tx_443
add counter inet portolan rx_20001
add counter inet portolan tx_20001
flush chain inet portolan input
flush chain inet portolan output
add rule inet portolan input counter name tcp dport map { 443 : "rx_443", 20001 : "rx_20001" }
add rule inet portolan output counter name tcp sport map { 443 : "tx_443", 20001 : "tx_20001" }
add rule inet portolan input counter name udp dport map { 443 : "rx_443", 20001 : "rx_20001" }
add rule inet portolan output counter name udp sport map { 443 : "tx_443", 20001 : "tx_20001" }
`
	if got != want {
		t.Fatalf("script:\n%s", got)
	}
}

const listing = `table inet portolan { # handle 12
	counter rx_443 { # handle 3
		packets 120 bytes 98765
	}
	counter tx_443 { # handle 4
		packets 240 bytes 1234567
	}
	counter rx_9000 { # handle 5
		packets 1 bytes 60
	}

	chain input { # handle 1
		type filter hook input priority 10; policy accept;
		counter name tcp dport map { 443 : "rx_443" } # handle 7
	}
}
`

func fakeNFT(t *testing.T, output string, calls *[]string) *Accounting {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "nft")
	if err := os.WriteFile(binary, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	return &Accounting{Binary: binary, Run: func(_ context.Context, stdin, _ string, args ...string) (string, error) {
		*calls = append(*calls, strings.Join(args, " ")+"|"+stdin)
		if args[0] == "-a" {
			return output, nil
		}
		return "", nil
	}}
}

func TestCountersReportBothDirectionsWithTableEpoch(t *testing.T) {
	t.Parallel()
	var calls []string
	accounting := fakeNFT(t, listing, &calls)
	epoch, counters, err := accounting.Counters(context.Background(), "boot-1", []uint16{443, 443, 9000, 0})
	if err != nil {
		t.Fatal(err)
	}
	if epoch != "boot-1:12" {
		t.Fatalf("epoch = %q", epoch)
	}
	// Port 9000 has no sent counter yet, so it is not reported.
	if want := []model.PortTraffic{{Port: 443, RX: 98765, TX: 1234567}}; !reflect.DeepEqual(counters, want) {
		t.Fatalf("counters = %#v", counters)
	}
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "-f /dev/stdin|"+Script([]uint16{443, 9000})) || calls[1] != "-a list table inet portolan|" {
		t.Fatalf("nft calls = %q", calls)
	}
}

func TestCountersLeaveHostsWithoutPortsUntouched(t *testing.T) {
	t.Parallel()
	var calls []string
	accounting := fakeNFT(t, listing, &calls)
	if _, counters, err := accounting.Counters(context.Background(), "boot-1", nil); err != nil || counters != nil || len(calls) != 0 {
		t.Fatalf("counters=%v err=%v calls=%q", counters, err, calls)
	}
	if _, _, err := accounting.Counters(context.Background(), "boot-1", []uint16{443}); err != nil {
		t.Fatal(err)
	}
	// Once rules exist, losing the last port empties them instead of leaving stale rules.
	calls = nil
	if _, counters, err := accounting.Counters(context.Background(), "boot-1", nil); err != nil || counters != nil {
		t.Fatalf("counters=%v err=%v", counters, err)
	}
	if len(calls) != 1 || !strings.Contains(calls[0], "flush chain inet portolan input") || strings.Contains(calls[0], "add rule") {
		t.Fatalf("nft calls = %q", calls)
	}
}

func TestReportExplainsMissingNFT(t *testing.T) {
	t.Parallel()
	collector := Collector{ProcRoot: t.TempDir(), Accounting: &Accounting{Binary: filepath.Join(t.TempDir(), "missing-nft")}}
	report := collector.Report(context.Background(), []uint16{443})
	if report.PortError != model.TrafficPortsUnavailable || len(report.Ports) != 0 || report.Interfaces == nil {
		t.Fatalf("report = %#v", report)
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("report is not acceptable to the panel: %v", err)
	}
}
