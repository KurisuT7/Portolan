package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/KurisuT7/Portolan/internal/model"
	"github.com/KurisuT7/Portolan/internal/vault"
	_ "modernc.org/sqlite"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrReadOnly     = errors.New("discovered nodes are read-only")
	ErrAgentOffline = errors.New("entry agent is offline")
	ErrInvalidInput = errors.New("invalid input")
	ErrConflict     = errors.New("resource conflict")
)

type Store struct {
	wakeMu  sync.Mutex
	waiters map[string]map[chan struct{}]struct{}
	db      *sql.DB
	vault   *vault.Vault
}

type NodeSummary struct {
	ID         string         `json:"id"`
	ServerID   string         `json:"server_id"`
	Name       string         `json:"name"`
	Protocol   model.Protocol `json:"protocol"`
	ListenPort uint16         `json:"listen_port"`
	Enabled    bool           `json:"enabled"`
	Managed    bool           `json:"managed"`
	Source     string         `json:"source,omitempty"`
	LastSeenAt time.Time      `json:"last_seen_at,omitempty"`
	Profile    string         `json:"profile"`
	CreatedAt  time.Time      `json:"created_at"`
}

type JobSummary struct {
	ID       string `json:"id"`
	ServerID string `json:"server_id"`
	Type     string `json:"type"`
	State    string `json:"state"`
	Result   string `json:"result,omitempty"`
	Revision int64  `json:"-"`
	// Applied compares a sync snapshot with the release the Agent reports as
	// active: current, behind, ahead, or empty when either side is unknown.
	Applied    string    `json:"applied,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

type ForwardProbeHistory struct {
	Summary ForwardProbeHistorySummary `json:"summary"`
	Points  []ForwardProbeHistoryPoint `json:"points"`
}

type ForwardProbeHistorySummary struct {
	AvailabilityPercent *float64  `json:"availability_percent"`
	AverageLatencyMS    float64   `json:"avg_latency_ms"`
	P95LatencyMS        float64   `json:"p95_latency_ms"`
	AverageJitterMS     float64   `json:"avg_jitter_ms"`
	LossPercent         float64   `json:"loss_percent"`
	Incidents           int       `json:"incidents"`
	SampleCount         int       `json:"sample_count"`
	From                time.Time `json:"from"`
	To                  time.Time `json:"to"`
}

type ForwardProbeHistoryPoint struct {
	CheckedAt    time.Time `json:"checked_at"`
	Attempts     int       `json:"attempts"`
	Successes    int       `json:"successes"`
	LatencyMS    float64   `json:"latency"`
	JitterMS     float64   `json:"jitter"`
	LossPercent  float64   `json:"loss"`
	Status       string    `json:"status"`
	Availability *float64  `json:"availability"`
	Reasons      []string  `json:"reasons,omitempty"`
}

type AgentNetworkUpdate struct {
	AddressesKnown bool
	IPv4Address    string
	IPv6Address    string
	EgressKnown    bool
	EgressIPv4     bool
	EgressIPv6     bool
}

func Open(path string, secretVault *vault.Vault) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, vault: secretVault}
	if err := store.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func randomID(prefix string) (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b), nil
}

func randomToken(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func IsConstraintError(err error) bool {
	return err != nil && (contains(err.Error(), "constraint failed") || contains(err.Error(), "UNIQUE constraint failed"))
}

func contains(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func (s NodeSummary) String() string {
	return fmt.Sprintf("%s/%s:%d", s.ServerID, s.Protocol, s.ListenPort)
}
