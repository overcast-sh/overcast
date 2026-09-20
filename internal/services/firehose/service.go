// Package firehose provides a basic emulation of Amazon Data Firehose
// (formerly Kinesis Data Firehose).
//
// Implemented operations: CreateDeliveryStream, DescribeDeliveryStream,
// ListDeliveryStreams, DeleteDeliveryStream, PutRecord, PutRecordBatch,
// TagDeliveryStream, UntagDeliveryStream, ListTagsForDeliveryStream.
//
// Records are validated as AWS validates them and then discarded: no
// destination configuration is stored and nothing is ever delivered.
package firehose

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const serviceName = "firehose"

// ─── Types ────────────────────────────────────────────────────

func (ds *DeliveryStream) GetTags() map[string]string  { return ds.Tags }
func (ds *DeliveryStream) SetTags(t map[string]string) { ds.Tags = t }

// DeliveryStream represents a Firehose delivery stream.
type DeliveryStream struct {
	DeliveryStreamName   string            `json:"DeliveryStreamName"`
	DeliveryStreamARN    string            `json:"DeliveryStreamARN"`
	DeliveryStreamStatus string            `json:"DeliveryStreamStatus"`
	DeliveryStreamType   string            `json:"DeliveryStreamType"`
	Tags                 map[string]string `json:"Tags,omitempty"`
}

// ─── Store ────────────────────────────────────────────────────

type firehoseStore struct {
	store state.Store
	cfg   *config.Config
}

func newFirehoseStore(s state.Store, cfg *config.Config) *firehoseStore {
	return &firehoseStore{store: s, cfg: cfg}
}

const nsStreams = "firehose:streams"

func (s *firehoseStore) putStream(ctx context.Context, ds *DeliveryStream) error {
	raw, err := json.Marshal(ds)
	if err != nil {
		return fmt.Errorf("firehose: marshal delivery stream: %w", err)
	}
	return s.store.Set(ctx, nsStreams, ds.DeliveryStreamName, string(raw))
}

func (s *firehoseStore) getStream(ctx context.Context, name string) (*DeliveryStream, bool) {
	raw, found, err := s.store.Get(ctx, nsStreams, name)
	if err != nil || !found {
		return nil, false
	}
	var ds DeliveryStream
	if json.Unmarshal([]byte(raw), &ds) != nil {
		return nil, false
	}
	return &ds, true
}

func (s *firehoseStore) listStreams(ctx context.Context) ([]*DeliveryStream, error) {
	pairs, err := s.store.Scan(ctx, nsStreams, "")
	if err != nil {
		return nil, err
	}
	out := make([]*DeliveryStream, 0, len(pairs))
	for _, kv := range pairs {
		var ds DeliveryStream
		if json.Unmarshal([]byte(kv.Value), &ds) == nil {
			out = append(out, &ds)
		}
	}
	return out, nil
}

func (s *firehoseStore) deleteStream(ctx context.Context, name string) error {
	return s.store.Delete(ctx, nsStreams, name)
}

// ─── Service ──────────────────────────────────────────────────

// Service implements router.Service and router.TargetDispatcher for Firehose.
type Service struct {
	log     *serviceutil.ServiceLogger
	store   *firehoseStore
	cfg     *config.Config
	ops     map[string]http.HandlerFunc
	typedOp map[string]op.Operation
}

// New returns a configured Firehose Service.
func New(cfg *config.Config, st state.Store, logger *zap.Logger, _ clock.Clock) *Service {
	s := &Service{
		log:   serviceutil.NewServiceLogger(logger, serviceName),
		store: newFirehoseStore(st, cfg),
		cfg:   cfg,
	}
	s.ops = map[string]http.HandlerFunc{
		"CreateDeliveryStream":      s.createDeliveryStream,
		"DescribeDeliveryStream":    s.describeDeliveryStream,
		"ListDeliveryStreams":       s.listDeliveryStreams,
		"DeleteDeliveryStream":      s.deleteDeliveryStream,
		"PutRecord":                 s.putRecord,
		"PutRecordBatch":            s.putRecordBatch,
		"TagDeliveryStream":         s.tagDeliveryStream,
		"UntagDeliveryStream":       s.untagDeliveryStream,
		"ListTagsForDeliveryStream": s.listTagsForDeliveryStream,
	}
	s.typedOp = s.typedOps()
	return s
}

func (s *Service) Name() string                { return serviceName }
func (s *Service) RegisterRoutes(_ chi.Router) {}
func (s *Service) TargetPrefix() string        { return "Firehose_20150804." }

func (s *Service) Dispatch(w http.ResponseWriter, r *http.Request) {
	if c, opName := codec.FromContext(r.Context()); c != nil && opName != "" {
		if !codec.Supports(s.SupportedProtocols(), c) {
			w.Header().Set("x-emulator-unsupported-protocol", c.Name())
			c.WriteError(w, r, &protocol.AWSError{
				Code: "UnsupportedProtocol", Message: "Firehose does not support wire protocol " + c.Name() + ".",
				HTTPStatus: http.StatusUnsupportedMediaType,
			})
			return
		}
		if c.Name() != codec.NameRPCv2CBOR {
			s.dispatchLegacy(w, r, opName)
			return
		}
		if typed, ok := s.typedOp[opName]; ok {
			typed.Invoke(w, r, c)
			return
		}
		c.WriteError(w, r, protocol.ErrNotImplemented)
		return
	}
	target := r.Header.Get("X-Amz-Target")
	opName := target
	if idx := strings.LastIndex(target, "."); idx >= 0 {
		opName = target[idx+1:]
	}
	s.dispatchLegacy(w, r, opName)
}

func (s *Service) dispatchLegacy(w http.ResponseWriter, r *http.Request, opName string) {
	if fn, ok := s.ops[opName]; ok {
		fn(w, r)
		return
	}
	protocol.NotImplementedJSON(w, r)
}

// ─── Handlers ─────────────────────────────────────────────────

// The legacy JSON1.0/1.1 handlers below decode the request and hand it
// straight to the typed implementation in typed_logic.go, which the CBOR path
// also calls. Neither validation nor response shaping lives here: the legacy
// copy used to re-implement both, which is how CreateDeliveryStream silently
// dropped Tags (#1196) and how PutRecordBatch acknowledged records it had
// never decoded (#149).

func (s *Service) createDeliveryStream(w http.ResponseWriter, r *http.Request) {
	var req createDeliveryStreamReq
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := s.createDeliveryStreamTyped(r.Context(), &req)
	writeTyped(w, r, resp, aerr)
}

func (s *Service) describeDeliveryStream(w http.ResponseWriter, r *http.Request) {
	var req describeDeliveryStreamReq
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := s.describeDeliveryStreamTyped(r.Context(), &req)
	writeTyped(w, r, resp, aerr)
}

func (s *Service) listDeliveryStreams(w http.ResponseWriter, r *http.Request) {
	resp, aerr := s.listDeliveryStreamsTyped(r.Context(), nil)
	writeTyped(w, r, resp, aerr)
}

func (s *Service) deleteDeliveryStream(w http.ResponseWriter, r *http.Request) {
	var req deleteDeliveryStreamReq
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := s.deleteDeliveryStreamTyped(r.Context(), &req)
	writeTyped(w, r, resp, aerr)
}

func (s *Service) putRecord(w http.ResponseWriter, r *http.Request) {
	var req putRecordReq
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := s.putRecordTyped(r.Context(), &req)
	writeTyped(w, r, resp, aerr)
}

func (s *Service) putRecordBatch(w http.ResponseWriter, r *http.Request) {
	var req putRecordBatchReq
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := s.putRecordBatchTyped(r.Context(), &req)
	writeTyped(w, r, resp, aerr)
}

// writeTyped renders a typed operation's result on the legacy JSON path: the
// modeled error, or the response body, which for an operation with an empty
// output shape is the empty JSON object AWS returns.
func writeTyped[T any](w http.ResponseWriter, r *http.Request, resp *T, aerr *protocol.AWSError) {
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

// ─── Tag handlers ───────────────────────────────────────────────

var firehoseTagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    "InvalidArgumentException",
	InvalidCode:     "InvalidArgumentException",
	ExceededMessage: "Too many tags.",
}

func (s *Service) tagDeliveryStream(w http.ResponseWriter, r *http.Request) {
	var req tagDeliveryStreamReq
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := s.tagDeliveryStreamTyped(r.Context(), &req)
	writeTyped(w, r, resp, aerr)
}

func (s *Service) untagDeliveryStream(w http.ResponseWriter, r *http.Request) {
	var req untagDeliveryStreamReq
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := s.untagDeliveryStreamTyped(r.Context(), &req)
	writeTyped(w, r, resp, aerr)
}

func (s *Service) listTagsForDeliveryStream(w http.ResponseWriter, r *http.Request) {
	var req listTagsForDeliveryStreamReq
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := s.listTagsForDeliveryStreamTyped(r.Context(), &req)
	writeTyped(w, r, resp, aerr)
}
