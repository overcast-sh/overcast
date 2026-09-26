package router

// samples.go implements POST /_overcast/samples/{dataset}, which loads a
// sample dataset for the console's Load sample dataset action. It runs the
// same internal/samples loader `overcast samples load` runs, with SDK
// clients served in process by the root router, so the load goes through the
// AWS API exactly as the CLI's does.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/samples"
	"github.com/overcast-sh/overcast/internal/sdkconfig"
	"github.com/overcast-sh/overcast/internal/services/athena"
)

// samplesPath is the sample-dataset endpoint.
const samplesPath = "/_overcast/samples/{dataset}"

// newSamplesHandler loads the dataset the path names, in the request's
// region, through root, and answers with the samples.Report. It refuses, 409,
// when a service the load goes through is not enabled.
func newSamplesHandler(cfg *config.Config, root http.Handler, enabled []string, engine func() athena.EngineStatus) http.HandlerFunc {
	engineFunc := func(context.Context) (athena.EngineStatus, error) { return engine(), nil }
	var missing []string
	for _, svc := range samples.RequiredServices {
		if !slices.Contains(enabled, svc) {
			missing = append(missing, svc)
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if len(missing) > 0 {
			writeDebugJSON(w, http.StatusConflict, map[string]string{
				"error": "the sample datasets need " + strings.Join(samples.RequiredServices, ", ") + " enabled; not enabled: " + strings.Join(missing, ", "),
			})
			return
		}
		region := middleware.RegionFromContext(r.Context(), cfg.Region)
		report, err := samples.NewLoader(sdkconfig.InProcess(root, region), engineFunc).Load(r.Context(), chi.URLParam(r, "dataset"))
		switch {
		case errors.Is(err, samples.ErrUnknownDataset):
			writeDebugJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		case err != nil:
			writeDebugJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		default:
			writeDebugJSON(w, http.StatusOK, report)
		}
	}
}
