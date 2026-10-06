package api

import (
	"crypto/sha256"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/KurisuT7/portolan/internal/buildinfo"
	"github.com/KurisuT7/portolan/internal/cores"
	"github.com/KurisuT7/portolan/internal/store"
)

const (
	maxBodySize               = 1 << 20
	agentAddressesHeader      = "X-Portolan-Public-Addresses"
	agentEgressFamiliesHeader = "X-Portolan-Egress-Families"
	maxAgentJobWait           = 60 * time.Second
	minAdminTokenLength       = 32
	minAdminTokenCharacters   = 8
)

type Server struct {
	store          *store.Store
	adminHash      [32]byte
	secureCookies  bool
	logger         *slog.Logger
	sessions       *sessionStore
	guard          *loginGuard
	trustedProxies []netip.Prefix
	publicURL      string
	downloadsDir   string
	regionLookup   func(string) (string, error)
	geoIPProvider  string
	cores          *cores.Client
	web            http.Handler
}

type Config struct {
	Store         *store.Store
	AdminToken    string
	SecureCookies bool
	Logger        *slog.Logger
	// TrustedProxies are the reverse proxies whose X-Forwarded-For is believed.
	// Nil means DefaultTrustedProxies.
	TrustedProxies []netip.Prefix
	PublicURL      string
	DownloadsDir   string
	RegionLookup   func(string) (string, error)
	// GeoIPProvider is "dbip" when the region database requires DB-IP attribution.
	GeoIPProvider string
	Cores         *cores.Client
	// Web serves the console. Nil serves only the API, for development with
	// the Vite dev server.
	Web http.Handler
}

// ValidateAdminToken rejects tokens that are short or made of few distinct
// characters. It is a guard against placeholders, not an entropy estimate.
func ValidateAdminToken(token string) error {
	distinct := map[rune]struct{}{}
	for _, character := range token {
		distinct[character] = struct{}{}
	}
	if len(token) < minAdminTokenLength || len(distinct) < minAdminTokenCharacters {
		return errors.New("the administrator token must be at least 32 characters with at least 8 different characters; generate one with: openssl rand -base64 32")
	}
	return nil
}

func New(config Config) (*Server, error) {
	if config.Store == nil || config.Cores == nil {
		return nil, errors.New("store and core releases are required")
	}
	if err := ValidateAdminToken(config.AdminToken); err != nil {
		return nil, err
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.TrustedProxies == nil {
		config.TrustedProxies = DefaultTrustedProxies
	}
	return &Server{
		store: config.Store, adminHash: sha256.Sum256([]byte(config.AdminToken)), secureCookies: config.SecureCookies,
		logger: config.Logger, sessions: &sessionStore{sessions: map[string]session{}}, guard: newLoginGuard(),
		trustedProxies: config.TrustedProxies, publicURL: strings.TrimRight(config.PublicURL, "/"),
		downloadsDir: config.DownloadsDir, regionLookup: config.RegionLookup, geoIPProvider: config.GeoIPProvider,
		cores: config.Cores, web: config.Web,
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /api/v1/session", s.login)
	mux.HandleFunc("DELETE /api/v1/session", s.withAdmin(s.logout))
	mux.HandleFunc("GET /api/v1/session", s.withAdmin(s.currentSession))
	mux.HandleFunc("POST /api/v1/security/totp/setup", s.withAdmin(s.setupTOTP))
	mux.HandleFunc("POST /api/v1/security/totp", s.withAdmin(s.enableTOTP))
	mux.HandleFunc("DELETE /api/v1/security/totp", s.withAdmin(s.disableTOTP))
	mux.HandleFunc("GET /api/v1/overview", s.withAdmin(s.overview))
	mux.HandleFunc("GET /api/v1/servers", s.withAdmin(s.listServers))
	mux.HandleFunc("POST /api/v1/servers", s.withAdmin(s.createServer))
	mux.HandleFunc("POST /api/v1/servers/{id}/enrollment", s.withAdmin(s.rotateServerEnrollment))
	mux.HandleFunc("POST /api/v1/servers/{id}/sync", s.withAdmin(s.syncServer))
	mux.HandleFunc("GET /api/v1/config-status", s.withAdmin(s.listConfigStatus))
	mux.HandleFunc("PATCH /api/v1/servers/{id}", s.withAdmin(s.updateServer))
	mux.HandleFunc("DELETE /api/v1/servers/{id}", s.withAdmin(s.deleteServer))
	mux.HandleFunc("GET /api/v1/nodes", s.withAdmin(s.listNodes))
	mux.HandleFunc("POST /api/v1/nodes", s.withAdmin(s.createNode))
	mux.HandleFunc("DELETE /api/v1/nodes/{id}", s.withAdmin(s.deleteNode))
	mux.HandleFunc("GET /api/v1/nodes/{id}/export", s.withAdmin(s.exportNode))
	mux.HandleFunc("GET /api/v1/forwards", s.withAdmin(s.listForwards))
	mux.HandleFunc("POST /api/v1/forwards", s.withAdmin(s.createForward))
	mux.HandleFunc("PUT /api/v1/forwards/{id}", s.withAdmin(s.updateForward))
	mux.HandleFunc("DELETE /api/v1/forwards/{id}", s.withAdmin(s.deleteForward))
	mux.HandleFunc("POST /api/v1/forwards/{id}/probe", s.withAdmin(s.triggerForwardProbe))
	mux.HandleFunc("GET /api/v1/forwards/{id}/probe-history", s.withAdmin(s.forwardProbeHistory))
	mux.HandleFunc("GET /api/v1/forward-probes", s.withAdmin(s.listForwardProbes))
	mux.HandleFunc("GET /api/v1/jobs", s.withAdmin(s.listJobs))
	mux.HandleFunc("GET /api/v1/cores", s.withAdmin(s.listCores))
	mux.HandleFunc("GET /api/v1/cores/{core}/releases", s.withAdmin(s.listCoreReleases))
	mux.HandleFunc("PUT /api/v1/cores/{core}", s.withAdmin(s.setCoreTarget))
	mux.HandleFunc("POST /api/v1/cores/{core}/rollout", s.withAdmin(s.rolloutCore))
	mux.HandleFunc("POST /api/v1/servers/{id}/cores/{core}", s.withAdmin(s.updateServerCore))
	mux.HandleFunc("POST /api/v1/agent/enroll", s.enrollAgent)
	mux.HandleFunc("GET /api/v1/agent/bootstrap/{token}", s.agentBootstrap)
	mux.HandleFunc("GET /api/v1/agent/downloads/{token}/{name}", s.agentDownload)
	mux.HandleFunc("GET /api/v1/agent/downloads/{token}/cores/{core}/{arch}", s.installerCoreDownload)
	mux.HandleFunc("GET /api/v1/agent/cores/{core}/{version}/{arch}", s.withAgent(s.agentCoreDownload))
	mux.HandleFunc("GET /api/v1/agent/jobs/next", s.withAgent(s.nextAgentJob))
	mux.HandleFunc("POST /api/v1/agent/jobs/{id}/complete", s.withAgent(s.completeAgentJob))
	mux.HandleFunc("GET /api/v1/agent/forwards", s.withAgent(s.agentForwards))
	mux.HandleFunc("POST /api/v1/agent/discovered-nodes", s.withAgent(s.saveAgentDiscoveredNodes))
	mux.HandleFunc("POST /api/v1/agent/forward-probes", s.withAgent(s.saveAgentForwardProbes))
	mux.HandleFunc("POST /api/v1/agent/status", s.withAgent(s.saveAgentStatus))
	if s.web != nil {
		// Unknown API paths stay JSON instead of falling through to the console.
		mux.HandleFunc("GET /api/", func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, "resource not found")
		})
		mux.Handle("GET /", s.web)
	}
	return s.securityHeaders(s.recoverer(s.accessLog(mux)))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "portolan-panel", "version": buildinfo.Version})
}

type sessionContextKey struct{}

type sessionTokenContextKey struct{}

type agentContextKey struct{}
