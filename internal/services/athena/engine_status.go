package athena

import (
	"net/http"
	"time"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// engineStatusPath is the emulator-only endpoint that reports the query
// engine's state, for the console's engine chip and for anyone wondering why
// the first query is slow.
const engineStatusPath = "/_overcast/athena/engine"

// engineStatus is what engineStatusPath reports.
type engineStatus struct {
	// Engine is ATHENA_ENGINE: trino or inert.
	Engine string `json:"engine"`
	// State is off, stopped, pulling, starting, ready or failed.
	State string `json:"state"`
	// Reason says why an engine is off.
	Reason      string `json:"reason,omitempty"`
	Image       string `json:"image,omitempty"`
	MemoryBytes int64  `json:"memoryBytes,omitempty"`
	ContainerID string `json:"containerId,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	// PullMillis and StartMillis are how long the last start spent pulling
	// the image and waiting for the engine to answer.
	PullMillis  int64     `json:"pullMillis,omitempty"`
	StartMillis int64     `json:"startMillis,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitzero"`
	LastUsedAt  time.Time `json:"lastUsedAt,omitzero"`
	// RunningQueries is how many queries hold the engine now.
	RunningQueries int    `json:"runningQueries"`
	LastError      string `json:"lastError,omitempty"`
}

// snapshot is the engine's current status.
func (m *engineManager) snapshot() engineStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.status
	s.Engine, s.Image, s.MemoryBytes, s.RunningQueries = string(config.AthenaEngineTrino), m.cfg.AthenaEngineImage, m.cfg.AthenaEngineMemory, m.inflight
	if m.docker == nil {
		s.Reason = "No Docker daemon is connected, so queries run inert: they succeed with empty results."
	}
	return s
}

func (s *Service) engineStatus() engineStatus {
	if s.engine == nil {
		return engineStatus{Engine: string(config.AthenaEngineInert), State: engineOff,
			Reason: "ATHENA_ENGINE=inert: queries succeed with empty results. DDL still updates the Glue Data Catalog."}
	}
	return s.engine.snapshot()
}

func (s *Service) serveEngineStatus(w http.ResponseWriter, r *http.Request) {
	protocol.WriteJSON(w, r, http.StatusOK, s.engineStatus())
}
