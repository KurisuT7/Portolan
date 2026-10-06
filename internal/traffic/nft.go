package traffic

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KurisuT7/Portolan/internal/model"
)

// Table is the nftables table that holds the port counters. Its chains only
// count packets: they accept everything and run after the standard filter
// priority, so they never change what other firewall rules decide, and
// packets those rules drop are not counted.
const Table = "portolan"

// ErrNFTMissing reports that the host has no nft command.
var ErrNFTMissing = errors.New("nft command not found")

var (
	tableHandle = regexp.MustCompile(`^table inet ` + Table + ` \{\s*#\s*handle\s+(\d+)`)
	counterHead = regexp.MustCompile(`^counter\s+"?([A-Za-z0-9_]+)"?\s*\{`)
	counterBody = regexp.MustCompile(`\bpackets\s+\d+\s+bytes\s+(\d+)`)
)

// Accounting keeps one received and one sent counter per port. Counters are
// named objects, so rewriting the rules keeps their values; they restart from
// zero only when the table is recreated, which changes the reported epoch.
type Accounting struct {
	// Binary is the nft command; empty looks it up in PATH and /usr/sbin.
	Binary string
	// Run executes a command with the given standard input. Tests replace it.
	Run func(ctx context.Context, stdin, name string, args ...string) (string, error)

	mu sync.Mutex
	// active records that this process installed rules that are still in place.
	active bool
}

// Counters makes the table count exactly the given ports and returns their
// values with the table's epoch. The rules are rewritten on every call, which
// restores them if another tool flushed the table.
func (a *Accounting) Counters(ctx context.Context, bootID string, ports []uint16) (string, []model.PortTraffic, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ports = slices.Clone(ports)
	slices.Sort(ports)
	ports = slices.Compact(ports)
	ports = slices.DeleteFunc(ports, func(port uint16) bool { return port == 0 })
	if len(ports) == 0 && !a.active {
		// A host without Portolan ports is left untouched.
		return "", nil, nil
	}
	binary, err := a.binary()
	if err != nil {
		return "", nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if output, err := a.run(ctx, Script(ports), binary, "-f", "/dev/stdin"); err != nil {
		return "", nil, fmt.Errorf("apply nftables counters: %w: %s", err, strings.TrimSpace(output))
	}
	a.active = len(ports) > 0
	if len(ports) == 0 {
		return "", nil, nil
	}
	output, err := a.run(ctx, "", binary, "-a", "list", "table", "inet", Table)
	if err != nil {
		return "", nil, fmt.Errorf("read nftables counters: %w: %s", err, strings.TrimSpace(output))
	}
	handle, values := parseTable(output)
	counters := make([]model.PortTraffic, 0, len(ports))
	for _, port := range ports {
		received, hasReceived := values[counterName("rx", port)]
		sent, hasSent := values[counterName("tx", port)]
		if hasReceived && hasSent {
			counters = append(counters, model.PortTraffic{Port: port, RX: received, TX: sent})
		}
	}
	return bootID + ":" + handle, counters, nil
}

// Script returns the nft commands that create the table when it is missing,
// add any missing counters and replace the rules. nft applies a script as one
// transaction.
func Script(ports []uint16) string {
	var script strings.Builder
	fmt.Fprintf(&script, "add table inet %s\n", Table)
	for _, hook := range []string{"input", "output"} {
		fmt.Fprintf(&script, "add chain inet %s %s { type filter hook %s priority 10 ; policy accept ; }\n", Table, hook, hook)
	}
	for _, port := range ports {
		fmt.Fprintf(&script, "add counter inet %s %s\n", Table, counterName("rx", port))
		fmt.Fprintf(&script, "add counter inet %s %s\n", Table, counterName("tx", port))
	}
	fmt.Fprintf(&script, "flush chain inet %s input\nflush chain inet %s output\n", Table, Table)
	if len(ports) == 0 {
		return script.String()
	}
	received := counterMap("rx", ports)
	sent := counterMap("tx", ports)
	for _, protocol := range []string{"tcp", "udp"} {
		fmt.Fprintf(&script, "add rule inet %s input counter name %s dport map { %s }\n", Table, protocol, received)
		fmt.Fprintf(&script, "add rule inet %s output counter name %s sport map { %s }\n", Table, protocol, sent)
	}
	return script.String()
}

func counterName(direction string, port uint16) string {
	return direction + "_" + strconv.Itoa(int(port))
}

func counterMap(direction string, ports []uint16) string {
	elements := make([]string, len(ports))
	for index, port := range ports {
		elements[index] = fmt.Sprintf("%d : %q", port, counterName(direction, port))
	}
	return strings.Join(elements, ", ")
}

// parseTable reads the table handle and the byte count of every named counter
// from `nft -a list table`.
func parseTable(output string) (handle string, counters map[string]uint64) {
	counters = map[string]uint64{}
	current := ""
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if match := tableHandle.FindStringSubmatch(line); match != nil {
			handle = match[1]
			continue
		}
		if match := counterHead.FindStringSubmatch(line); match != nil {
			current = match[1]
		}
		if current == "" {
			continue
		}
		if match := counterBody.FindStringSubmatch(line); match != nil {
			if value, err := strconv.ParseUint(match[1], 10, 64); err == nil {
				counters[current] = value
			}
			current = ""
		}
	}
	return handle, counters
}

func (a *Accounting) binary() (string, error) {
	candidates := []string{a.Binary}
	if a.Binary == "" {
		if path, err := exec.LookPath("nft"); err == nil {
			return path, nil
		}
		// systemd services do not always have the sbin directories in PATH.
		candidates = []string{"/usr/sbin/nft", "/sbin/nft"}
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", ErrNFTMissing
}

func (a *Accounting) run(ctx context.Context, stdin, name string, args ...string) (string, error) {
	if a.Run != nil {
		return a.Run(ctx, stdin, name, args...)
	}
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = strings.NewReader(stdin)
	output, err := command.CombinedOutput()
	return string(output), err
}
