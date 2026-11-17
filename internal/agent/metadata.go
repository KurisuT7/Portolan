package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/KurisuT7/Portolan/internal/model"
)

const (
	defaultMetadataBaseURL = "http://169.254.169.254"
	metadataPositiveTTL    = 2 * time.Minute
	metadataNegativeTTL    = 15 * time.Minute
	metadataMaxBodyBytes   = 256
)

var errMetadataUnreachable = errors.New("instance metadata endpoint is unreachable")

// metadataDetector discovers a 1:1 NAT cloud instance's public IPv4 from its
// link-local metadata service without calling external internet services.
type metadataDetector struct {
	baseURL string
	client  *http.Client
	now     func() time.Time

	mu       sync.Mutex
	cachedIP string
	provider string
	expires  time.Time
}

func (d *metadataDetector) publicIPv4(ctx context.Context) string {
	nowFn := d.now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()

	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.expires.IsZero() && now.Before(d.expires) {
		return d.cachedIP
	}

	ip, matched := d.probe(ctx)
	d.cachedIP = ip
	if matched {
		d.expires = nowFn().Add(metadataPositiveTTL)
	} else {
		d.provider = ""
		d.expires = nowFn().Add(metadataNegativeTTL)
	}
	return d.cachedIP
}

func (d *metadataDetector) probe(ctx context.Context) (string, bool) {
	order := []string{"gcp", "aws", "azure"}
	if d.provider != "" {
		ordered := make([]string, 0, len(order))
		ordered = append(ordered, d.provider)
		for _, candidate := range order {
			if candidate != d.provider {
				ordered = append(ordered, candidate)
			}
		}
		order = ordered
	}

	for _, provider := range order {
		ip, matched, err := d.probeProvider(ctx, provider)
		if errors.Is(err, errMetadataUnreachable) {
			return "", false
		}
		if matched {
			d.provider = provider
			return ip, true
		}
	}
	return "", false
}

func (d *metadataDetector) probeProvider(ctx context.Context, provider string) (string, bool, error) {
	switch provider {
	case "gcp":
		return d.probeGCP(ctx)
	case "aws":
		return d.probeAWS(ctx)
	case "azure":
		return d.probeAzure(ctx)
	default:
		return "", false, nil
	}
}

func (d *metadataDetector) probeGCP(ctx context.Context) (string, bool, error) {
	status, headers, body, err := d.do(ctx, http.MethodGet,
		"/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip",
		map[string]string{"Metadata-Flavor": "Google"})
	if err != nil {
		return "", false, err
	}
	if !strings.EqualFold(headers.Get("Metadata-Flavor"), "Google") {
		return "", false, nil
	}
	if status == http.StatusOK {
		return parseMetadataIPv4(body), true, nil
	}
	if status == http.StatusNotFound {
		return "", true, nil
	}
	return "", false, nil
}

func (d *metadataDetector) probeAWS(ctx context.Context) (string, bool, error) {
	tokenStatus, _, tokenBody, err := d.do(ctx, http.MethodPut, "/latest/api/token",
		map[string]string{"X-aws-ec2-metadata-token-ttl-seconds": "60"})
	if err != nil {
		return "", false, err
	}

	var headers map[string]string
	if tokenStatus == http.StatusOK {
		token := strings.TrimSpace(tokenBody)
		if token == "" {
			return "", false, nil
		}
		headers = map[string]string{"X-aws-ec2-metadata-token": token}
	} else if tokenStatus != http.StatusNotFound && tokenStatus != http.StatusMethodNotAllowed {
		return "", false, nil
	}

	status, _, body, err := d.do(ctx, http.MethodGet, "/latest/meta-data/public-ipv4", headers)
	if err != nil {
		return "", false, err
	}
	if tokenStatus == http.StatusOK {
		if status == http.StatusOK {
			return parseMetadataIPv4(body), true, nil
		}
		if status == http.StatusNotFound {
			return "", true, nil
		}
		return "", false, nil
	}
	if status == http.StatusOK {
		if ip := parseMetadataIPv4(body); ip != "" {
			return ip, true, nil
		}
	}
	return "", false, nil
}

func (d *metadataDetector) probeAzure(ctx context.Context) (string, bool, error) {
	status, headers, body, err := d.do(ctx, http.MethodGet,
		"/metadata/instance/network/interface/0/ipv4/ipAddress/0/publicIpAddress?api-version=2021-02-01&format=text",
		map[string]string{"Metadata": "true"})
	if err != nil {
		return "", false, err
	}
	if status != http.StatusOK {
		return "", false, nil
	}
	if ip := parseMetadataIPv4(body); ip != "" {
		return ip, true, nil
	}
	contentType := strings.ToLower(headers.Get("Content-Type"))
	if strings.TrimSpace(body) == "" && !strings.Contains(contentType, "html") {
		return "", true, nil
	}
	return "", false, nil
}

func (d *metadataDetector) do(ctx context.Context, method, path string, headers map[string]string) (int, http.Header, string, error) {
	base := strings.TrimRight(d.baseURL, "/")
	if base == "" {
		base = defaultMetadataBaseURL
	}
	request, err := http.NewRequestWithContext(ctx, method, base+path, nil)
	if err != nil {
		return 0, nil, "", err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := d.httpClient().Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, "", err
		}
		return 0, nil, "", errMetadataUnreachable
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, metadataMaxBodyBytes))
	if err != nil {
		return 0, nil, "", err
	}
	return response.StatusCode, response.Header, string(payload), nil
}

func (d *metadataDetector) httpClient() *http.Client {
	if d.client != nil {
		return d.client
	}
	d.client = &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			Proxy:                 nil,
			DisableKeepAlives:     true,
			DialContext:           (&net.Dialer{Timeout: 250 * time.Millisecond}).DialContext,
			ResponseHeaderTimeout: 300 * time.Millisecond,
		},
	}
	return d.client
}

func parseMetadataIPv4(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.ContainsAny(trimmed, "<>") {
		return ""
	}
	fields := strings.Fields(trimmed)
	if len(fields) != 1 {
		return ""
	}
	address, err := netip.ParseAddr(fields[0])
	if err != nil || !address.Is4() || !model.IsPublicRoutableIP(address.String()) {
		return ""
	}
	return address.String()
}
