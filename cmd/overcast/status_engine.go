package main

// status_engine.go — the Athena engine line `overcast status` prints from
// the athenaEngine field of /_overcast/health.

import (
	"fmt"
	"strings"
	"time"

	athenasvc "github.com/overcast-sh/overcast/internal/services/athena"
)

// athenaEngineLine describes the engine: its state, the memory it is given
// and, while it is up, for how long.
func athenaEngineLine(st athenasvc.EngineStatus) string {
	memory := "memory " + formatGiB(st.MemoryBytes)
	var parts []string
	switch st.State {
	case athenasvc.EngineOff:
		parts = []string{"off, queries run inert"}
	case athenasvc.EngineProbing:
		parts = []string{"waiting for Docker"}
	case athenasvc.EngineStopped:
		parts = []string{"stopped-idle", memory, "the next query starts it"}
	case athenasvc.EnginePulling:
		parts = []string{"starting (pulling " + imageName(st.Image) + ")", memory}
	case athenasvc.EngineStarting:
		parts = []string{"starting", memory}
	case athenasvc.EngineReady:
		parts = []string{"ready", memory, "up " + (time.Duration(st.UptimeMillis) * time.Millisecond).Round(time.Second).String()}
		if st.RunningQueries == 1 {
			parts = append(parts, "1 query running")
		} else if st.RunningQueries > 1 {
			parts = append(parts, fmt.Sprintf("%d queries running", st.RunningQueries))
		}
	case athenasvc.EngineFailed:
		parts = []string{"failed to start: " + st.LastError}
	default:
		parts = []string{st.State}
	}
	return "athena engine: " + strings.Join(parts, ", ")
}

// imageName is an image reference without its digest: the tag says which
// engine, and the pinned digest after it says nothing more to a reader.
func imageName(ref string) string {
	name, _, _ := strings.Cut(ref, "@")
	return name
}

func formatGiB(bytes int64) string {
	return fmt.Sprintf("%.1f GiB", float64(bytes)/(1<<30))
}
