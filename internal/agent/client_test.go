package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KurisuT7/Portolan/internal/model"
)

type staticAddress string

func (address staticAddress) Network() string { return "ip" }
func (address staticAddress) String() string  { return string(address) }

func TestNetworkReportSeparatesPublicIngressFromEgressStack(t *testing.T) {
	t.Parallel()
	report := networkReportFromAddresses([]net.Addr{
		staticAddress("127.0.0.1/8"),
		staticAddress("100.88.13.252/20"),
		staticAddress("fe80::1/64"),
		staticAddress("2001:db8:100:dfd::/64"),
	})

	if !reflect.DeepEqual(report.PublicAddresses, []string{"2001:db8:100:dfd::"}) {
		t.Fatalf("public ingress addresses = %#v", report.PublicAddresses)
	}
	if !reflect.DeepEqual(report.EgressFamilies, []string{"ipv4", "ipv6"}) {
		t.Fatalf("egress families = %#v", report.EgressFamilies)
	}
}

func TestHeaderListReportsKnownEmptyValue(t *testing.T) {
	t.Parallel()
	if got := headerList(nil); got != "none" {
		t.Fatalf("headerList(nil) = %q", got)
	}
}

func TestContinuousLoopsKeepProbingWhileJobPollWaitsAndReuseForwardList(t *testing.T) {
	t.Parallel()
	var forwardLists atomic.Int32
	var probeReports atomic.Int32
	jobStarted := make(chan struct{})
	twoReports := make(chan struct{})
	var jobStartedOnce sync.Once
	var twoReportsOnce sync.Once
	assigned := model.Forward{
		ID: "fwd_udp", Networks: []string{"udp"}, TargetHost: "127.0.0.1", TargetPort: 53, Enabled: true,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/jobs/next":
			jobStartedOnce.Do(func() { close(jobStarted) })
			<-r.Context().Done()
		case "/api/v1/agent/forwards":
			forwardLists.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []model.Forward{assigned}})
		case "/api/v1/agent/forward-probes":
			if probeReports.Add(1) >= 2 {
				twoReportsOnce.Do(func() { close(twoReports) })
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/agent/discovered-nodes":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := New(Config{
		PanelURL: server.URL, ServerID: "srv_test", AgentToken: strings.Repeat("a", 32),
		RuntimeRoot: t.TempDir(), PollInterval: 20 * time.Millisecond, AllowInsecureHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx, false) }()

	select {
	case <-jobStarted:
	case <-ctx.Done():
		t.Fatal("job long poll did not start")
	}
	select {
	case <-twoReports:
	case <-ctx.Done():
		t.Fatal("periodic probes did not continue during the job long poll")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v after cancellation", err)
	}
	if got := forwardLists.Load(); got != 1 {
		t.Fatalf("forward list requests = %d, want 1 while the cache is fresh", got)
	}
}

func TestStatusReportCarriesRuntimeObservation(t *testing.T) {
	t.Parallel()
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/status" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 32) {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := New(Config{
		PanelURL: server.URL, ServerID: "srv_test", AgentToken: strings.Repeat("a", 32), RuntimeRoot: t.TempDir(),
		SingBoxBinary: "missing-sing-box", RealmBinary: "missing-realm", AllowInsecureHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.reportStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"applied_revision": float64(0), "agent_version": "dev", "sing_box_version": "", "realm_version": "", "units": []any{}}
	if !reflect.DeepEqual(received, want) {
		t.Fatalf("status body = %#v", received)
	}
}
