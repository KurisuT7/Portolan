package agent

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/KurisuT7/portolan/internal/model"
)

func TestProbeForwardReportsStableAndUDPUnsupported(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_ = connection.Close()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	probe := probeForward(context.Background(), model.Forward{ID: "fwd_test", TargetHost: "127.0.0.1", TargetPort: uint16(port), Networks: []string{"tcp"}})
	if probe.Status != "stable" || probe.Successes != 3 || probe.LossPct != 0 {
		t.Fatalf("unexpected TCP probe: %#v (port %s)", probe, strconv.Itoa(port))
	}
	udp := probeForward(context.Background(), model.Forward{ID: "fwd_udp", TargetHost: "127.0.0.1", TargetPort: uint16(port), Networks: []string{"udp"}})
	if udp.Status != "unsupported" {
		t.Fatalf("generic UDP must not claim reachability: %#v", udp)
	}
}

func TestProbeForwardSupportsIPv6Target(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback is unavailable: %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_ = connection.Close()
		}
	}()
	host, rawPort, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(rawPort, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	probe := probeForward(context.Background(), model.Forward{
		ID: "fwd_ipv6", TargetHost: "[" + host + "]", TargetPort: uint16(port), Networks: []string{"tcp"},
	})
	if probe.Status != "stable" || probe.Successes != 3 || probe.LossPct != 0 {
		t.Fatalf("unexpected IPv6 TCP probe: %#v", probe)
	}
}
