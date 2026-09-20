package secretsmanager

import (
	"net/http"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// ─── Stubs (not implemented) ───────────────────────────────────────────────
//
// Replication is inherently multi-region and out of scope for a single-node
// emulator — see docs/plans/full-emulation-priority.md §7.

func (h *Handler) ReplicateSecretToRegions(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedJSON(w, r)
}

func (h *Handler) RemoveRegionsFromReplication(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedJSON(w, r)
}
