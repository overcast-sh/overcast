package acm

import (
	"context"
	"net/http"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// Handler holds ACM handler dependencies.
type Handler struct {
	ops     map[string]http.HandlerFunc
	typedOp map[string]op.Operation
	store   *acmStore
	cfg     *config.Config
	clk     clock.Clock
}

func newHandler(cfg *config.Config, store *acmStore, clk clock.Clock) *Handler {
	h := &Handler{cfg: cfg, store: store, clk: clk}
	h.initOps()
	return h
}

func (h *Handler) initOps() {
	// Every operation is implemented once, as a typed function, and reached
	// from the JSON path through an adapter. Keeping two copies is how the tag
	// handlers drifted into not checking that the certificate exists — and how
	// DescribeCertificate, ListCertificates and DeleteCertificate kept a
	// second, filter-blind copy of the read path until #1994 removed it.
	h.ops = map[string]http.HandlerFunc{
		"DescribeCertificate":              serveTyped(h.describeCertificateTyped),
		"ListCertificates":                 serveTyped(h.listCertificatesTyped),
		"DeleteCertificate":                serveTyped(h.deleteCertificateTyped),
		"RequestCertificate":               serveTyped(h.requestCertificateTyped),
		"ListCertificateDomainValidations": serveTyped(h.listCertificateDomainValidationsTyped),
		"ListTagsForCertificate":           serveTyped(h.listTagsForCertificateTyped),
		"AddTagsToCertificate":             serveTyped(h.addTagsToCertificateTyped),
		"RemoveTagsFromCertificate":        serveTyped(h.removeTagsFromCertificateTyped),
		"TagResource":                      serveTyped(h.tagResourceTyped),
		"UntagResource":                    serveTyped(h.untagResourceTyped),
		"ListTagsForResource":              serveTyped(h.listTagsForResourceTyped),
	}
	h.typedOp = h.typedOps()
}

// serveTyped adapts a typed operation to the JSON 1.0/1.1 dispatch table.
func serveTyped[In any, Out any](fn func(context.Context, *In) (*Out, *protocol.AWSError)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in In
		if !serviceutil.DecodeJSON(w, r, &in) {
			return
		}
		out, aerr := fn(r.Context(), &in)
		if aerr != nil {
			protocol.WriteJSONError(w, r, aerr)
			return
		}
		if out == nil {
			protocol.WriteJSON(w, r, http.StatusOK, map[string]any{})
			return
		}
		protocol.WriteJSON(w, r, http.StatusOK, out)
	}
}
