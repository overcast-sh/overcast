package eks

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/overcast-sh/overcast/internal/config"
)

const eksTestNetwork = "overcast"

func liveTestConfig(hostname string) *config.Config {
	return &config.Config{
		Region:    "us-east-1",
		AccountID: "000000000000",
		Hostname:  hostname,
		Network:   eksTestNetwork,
		EKSMode:   config.EKSModeLive,
	}
}

// allowControlPlaneConnect keeps Docker fakes focused on the operation each
// test exercises. Containerised test runs attach their own container to the
// control plane and inspect the target for an address there; host runs do
// neither. These fakes deliberately report no such address so both modes use
// the published-port path the tests already model.
//
// Every other network connect is accepted and not recorded as well: a control
// plane adopted after a restart is joined to its planes on the way, and which
// planes those are is live_runtime_dataplane_test.go's concern, not these
// tests'.
func allowControlPlaneConnect(next http.HandlerFunc) http.HandlerFunc {
	var addressInspectPending atomic.Bool
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost &&
			strings.HasPrefix(r.URL.Path, "/v1.45/networks/") &&
			strings.HasSuffix(r.URL.Path, "/connect") {
			if strings.HasSuffix(r.URL.Path, "_control/connect") {
				addressInspectPending.Store(true)
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodGet &&
			strings.HasPrefix(r.URL.Path, "/v1.45/containers/") &&
			strings.HasSuffix(r.URL.Path, "/json") &&
			addressInspectPending.CompareAndSwap(true, false) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"NetworkSettings": map[string]any{"Networks": map[string]any{}},
			})
			return
		}
		next(w, r)
	}
}
