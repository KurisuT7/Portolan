//go:build linux

package traffic

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KurisuT7/Portolan/internal/model"
)

// TestNFTAccountingCountsLoopbackTraffic runs against the real nft command. It
// changes the ruleset, so it only runs with PORTOLAN_NFT_TEST=1, as root in a
// network namespace of its own:
//
//	go test -c -o traffic.test ./internal/traffic
//	sudo unshare --net sh -c 'ip link set lo up && PORTOLAN_NFT_TEST=1 ./traffic.test -test.run NFT -test.v'
func TestNFTAccountingCountsLoopbackTraffic(t *testing.T) {
	if os.Getenv("PORTOLAN_NFT_TEST") != "1" {
		t.Skip("set PORTOLAN_NFT_TEST=1 to run against the real nft in a disposable network namespace")
	}
	ctx := context.Background()
	accounting := &Accounting{}
	binary, err := accounting.binary()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command(binary, "delete", "table", "inet", Table).Run() })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	udp, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()

	epoch, counters, err := accounting.Counters(ctx, "boot", []uint16{port})
	if err != nil {
		t.Fatal(err)
	}
	if len(counters) != 1 || counters[0].RX != 0 || counters[0].TX != 0 || !strings.HasPrefix(epoch, "boot:") || epoch == "boot:" {
		t.Fatalf("fresh counters = %#v epoch=%q", counters, epoch)
	}

	// The client sends 200 KB and receives 50 KB back over TCP, then sends one
	// UDP datagram.
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		_, _ = io.CopyN(io.Discard, connection, 200_000)
		_, _ = connection.Write(make([]byte, 50_000))
	}()
	connection, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write(make([]byte, 200_000)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(io.Discard, connection, 50_000); err != nil {
		t.Fatal(err)
	}
	connection.Close()
	sender, err := net.Dial("udp", udp.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sender.Write(make([]byte, 1_000)); err != nil {
		t.Fatal(err)
	}
	sender.Close()
	time.Sleep(200 * time.Millisecond)

	again, counters, err := accounting.Counters(ctx, "boot", []uint16{port, 9})
	if err != nil {
		t.Fatal(err)
	}
	var counted model.PortTraffic
	for _, item := range counters {
		if item.Port == port {
			counted = item
		}
	}
	// Packet sizes include IP and transport headers.
	if again != epoch || counted.RX < 201_000 || counted.RX > 260_000 || counted.TX < 50_000 || counted.TX > 80_000 {
		t.Fatalf("counted %#v of %#v with epoch %q (was %q)", counted, counters, again, epoch)
	}

	// Rewriting the rules keeps the named counters.
	if _, kept, err := accounting.Counters(ctx, "boot", []uint16{port}); err != nil || len(kept) != 1 || kept[0].RX < counted.RX {
		t.Fatalf("after rewrite = %#v err=%v", kept, err)
	}
	// A recreated table restarts the counters under a new epoch.
	if output, err := exec.Command(binary, "delete", "table", "inet", Table).CombinedOutput(); err != nil {
		t.Fatalf("delete table: %v: %s", err, output)
	}
	recreated, counters, err := accounting.Counters(ctx, "boot", []uint16{port})
	if err != nil || recreated == epoch || len(counters) != 1 || counters[0].RX != 0 {
		t.Fatalf("after recreation = %#v epoch=%q err=%v", counters, recreated, err)
	}
	// Without ports the rules are emptied and nothing is counted.
	if _, counters, err := accounting.Counters(ctx, "boot", nil); err != nil || counters != nil {
		t.Fatalf("without ports = %#v err=%v", counters, err)
	}
	output, err := exec.Command(binary, "list", "chain", "inet", Table, "input").CombinedOutput()
	if err != nil || strings.Contains(string(output), "dport") {
		t.Fatalf("input chain after removing all ports: %v: %s", err, output)
	}
}
