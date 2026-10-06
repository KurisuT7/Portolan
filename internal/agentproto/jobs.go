// Package agentproto defines the version-compatible control-plane messages sent
// to Agents. It has no persistence or HTTP dependencies.
package agentproto

import (
	"encoding/json"
	"time"

	"github.com/KurisuT7/Portolan/internal/model"
)

type Job struct {
	ID        string          `json:"id"`
	ServerID  string          `json:"server_id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type SyncPayload struct {
	Revision int64           `json:"revision"`
	Nodes    []model.Node    `json:"nodes"`
	Forwards []model.Forward `json:"forwards"`
}

type ProbeJobPayload struct {
	ForwardID string `json:"forward_id"`
}

// CoreUpdatePayload asks an Agent to install a core release hosted by the
// panel. SHA256 holds the archive digest by GOARCH.
type CoreUpdatePayload struct {
	Version string            `json:"version"`
	SHA256  map[string]string `json:"sha256"`
}
