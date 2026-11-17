package agent

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestNetworkReportDetectsGCPMetadataPublicIPv4ForDualStackNAT(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip" &&
			r.Header.Get("Metadata-Flavor") == "Google" {
			w.Header().Set("Metadata-Flavor", "Google")
			_, _ = w.Write([]byte("198.51.100.34"))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	detector := &metadataDetector{baseURL: server.URL}
	report := networkReportWithMetadata(context.Background(), []net.Addr{
		staticAddress("10.128.0.2/20"),
		staticAddress("2001:db8:100::1/64"),
	}, detector)

	if !reflect.DeepEqual(report.PublicAddresses, []string{"198.51.100.34", "2001:db8:100::1"}) {
		t.Fatalf("public addresses = %#v", report.PublicAddresses)
	}
	if !reflect.DeepEqual(report.EgressFamilies, []string{"ipv4", "ipv6"}) {
		t.Fatalf("egress families = %#v", report.EgressFamilies)
	}
}

func TestGCPMetadataRequiresResponseHeader(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip" {
			_, _ = w.Write([]byte("198.51.100.34"))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	detector := &metadataDetector{baseURL: server.URL}
	if got := detector.publicIPv4(context.Background()); got != "" {
		t.Fatalf("expected empty IP when Metadata-Flavor response header is absent, got %q", got)
	}
}

func TestAWSMetadataIMDSv2AndCaching(t *testing.T) {
	t.Parallel()
	var tokenRequests atomic.Int32
	var ipRequests atomic.Int32
	var currentIP atomic.Value
	currentIP.Store("203.0.113.42")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/latest/api/token":
			if r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds") == "" {
				http.Error(w, "missing ttl", http.StatusBadRequest)
				return
			}
			tokenRequests.Add(1)
			_, _ = w.Write([]byte("imds-v2-session-token"))
		case r.Method == http.MethodGet && r.URL.Path == "/latest/meta-data/public-ipv4":
			if r.Header.Get("X-aws-ec2-metadata-token") != "imds-v2-session-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			ipRequests.Add(1)
			_, _ = w.Write([]byte(currentIP.Load().(string)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	now := time.Unix(1700000000, 0)
	detector := &metadataDetector{
		baseURL: server.URL,
		now:     func() time.Time { return now },
	}
	if got := detector.publicIPv4(context.Background()); got != "203.0.113.42" {
		t.Fatalf("first AWS lookup = %q, want 203.0.113.42", got)
	}
	if got := detector.publicIPv4(context.Background()); got != "203.0.113.42" {
		t.Fatalf("cached AWS lookup = %q, want 203.0.113.42", got)
	}
	if tokenRequests.Load() != 1 || ipRequests.Load() != 1 {
		t.Fatalf("expected 1 probe before TTL expiry, got tokens=%d ips=%d", tokenRequests.Load(), ipRequests.Load())
	}

	now = now.Add(metadataPositiveTTL + time.Second)
	currentIP.Store("203.0.113.88")
	if got := detector.publicIPv4(context.Background()); got != "203.0.113.88" {
		t.Fatalf("refreshed AWS lookup = %q, want 203.0.113.88", got)
	}
}

func TestAzureMetadataPublicIPv4(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata/instance/network/interface/0/ipv4/ipAddress/0/publicIpAddress" &&
			r.URL.Query().Get("api-version") == "2021-02-01" &&
			r.URL.Query().Get("format") == "text" &&
			r.Header.Get("Metadata") == "true" {
			_, _ = w.Write([]byte("198.51.100.77\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	detector := &metadataDetector{baseURL: server.URL}
	if got := detector.publicIPv4(context.Background()); got != "198.51.100.77" {
		t.Fatalf("Azure lookup = %q, want 198.51.100.77", got)
	}
}

func TestMetadataRejectsNonPublicAndHTMLPayloads(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{"10.0.0.5", "100.64.1.2", "192.168.1.10", "2001:db8::1", "<html>not found</html>"} {
		payload := payload
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip" {
				w.Header().Set("Metadata-Flavor", "Google")
				_, _ = w.Write([]byte(payload))
				return
			}
			http.NotFound(w, r)
		}))
		detector := &metadataDetector{baseURL: server.URL}
		got := detector.publicIPv4(context.Background())
		server.Close()
		if got != "" {
			t.Fatalf("payload %q produced %q, want empty", payload, got)
		}
	}
}

func TestMetadataSkippedWhenPublicIPv4ExistsOnInterface(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.NotFound(w, r)
	}))
	defer server.Close()

	detector := &metadataDetector{baseURL: server.URL}
	report := networkReportWithMetadata(context.Background(), []net.Addr{
		staticAddress("198.51.100.10/24"),
	}, detector)

	if !reflect.DeepEqual(report.PublicAddresses, []string{"198.51.100.10"}) {
		t.Fatalf("public addresses = %#v", report.PublicAddresses)
	}
	if requests.Load() != 0 {
		t.Fatalf("metadata server received %d requests when interface already had public IPv4", requests.Load())
	}
}
