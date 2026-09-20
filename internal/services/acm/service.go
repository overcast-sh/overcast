// Package acm provides a basic emulation of AWS Certificate Manager (ACM).
//
// Implemented operations: RequestCertificate, DescribeCertificate,
// ListCertificates, ListCertificateDomainValidations, DeleteCertificate,
// ListTagsForCertificate, AddTagsToCertificate, RemoveTagsFromCertificate.
//
// Certificates are issued immediately in ISSUED state — no validation required.
package acm

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const serviceName = "acm"

// awsapiService is ACM's key in the generated AWS model corpus.
// serviceutil.MustAWSService validates it at package initialisation, so a
// key the models do not carry fails immediately rather than silently
// answering every unimplemented operation with a 400.
var awsapiService = serviceutil.MustAWSService(serviceName)

// Certificate represents an ACM certificate. It is both the stored record and
// the CertificateDetail DescribeCertificate returns, so every member here is
// one the AWS model carries.
type Certificate struct {
	CertificateArn          string             `json:"CertificateArn"`
	DomainName              string             `json:"DomainName"`
	SubjectAlternativeNames []string           `json:"SubjectAlternativeNames,omitempty"`
	DomainValidationOptions []DomainValidation `json:"DomainValidationOptions,omitempty"`
	Status                  string             `json:"Status"`
	Type                    string             `json:"Type"`
	CreatedAt               float64            `json:"CreatedAt"`
	IssuedAt                float64            `json:"IssuedAt,omitempty"`
}

// DomainValidation mirrors ACM's DomainValidation shape
// (https://docs.aws.amazon.com/acm/latest/APIReference/API_DomainValidation.html):
// what the initial validation of one domain on the certificate looked like.
//
// ValidationEmails and HttpRedirect are the two modeled members Overcast never
// populates, so they are absent from the struct rather than always-empty
// fields. Overcast sends no validation mail, and reporting addresses it never
// wrote to would be a fiction a caller cannot check; HttpRedirect exists only
// for HTTP validation of CloudFront-issued certificates.
type DomainValidation struct {
	DomainName       string          `json:"DomainName"`
	ValidationDomain string          `json:"ValidationDomain,omitempty"`
	ValidationStatus string          `json:"ValidationStatus,omitempty"`
	ValidationMethod string          `json:"ValidationMethod,omitempty"`
	ResourceRecord   *ResourceRecord `json:"ResourceRecord,omitempty"`
}

// ResourceRecord is the CNAME a caller publishes to validate a domain by DNS
// (https://docs.aws.amazon.com/acm/latest/APIReference/API_ResourceRecord.html).
type ResourceRecord struct {
	Name  string `json:"Name"`
	Type  string `json:"Type"`
	Value string `json:"Value"`
}

// Tag is an ACM resource tag.
type Tag struct {
	Key   string `json:"Key"`
	Value string `json:"Value,omitempty"`
}

// ─── Store ────────────────────────────────────────────────────

type acmStore struct {
	store state.Store
	cfg   *config.Config
	clk   clock.Clock
}

func newACMStore(s state.Store, cfg *config.Config, clk clock.Clock) *acmStore {
	return &acmStore{store: s, cfg: cfg, clk: clk}
}

const (
	nsCerts = "acm:certs"
	nsTags  = "acm:tags"
)

func (s *acmStore) putCert(ctx context.Context, c *Certificate) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.store.Set(ctx, nsCerts, c.CertificateArn, string(raw))
}

func (s *acmStore) getCert(ctx context.Context, arn string) (*Certificate, bool) {
	raw, found, err := s.store.Get(ctx, nsCerts, arn)
	if err != nil || !found {
		return nil, false
	}
	var c Certificate
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, false
	}
	return &c, true
}

func (s *acmStore) listCerts(ctx context.Context) ([]*Certificate, error) {
	pairs, err := s.store.Scan(ctx, nsCerts, "")
	if err != nil {
		return nil, err
	}
	out := make([]*Certificate, 0, len(pairs))
	for _, kv := range pairs {
		var c Certificate
		if err := json.Unmarshal([]byte(kv.Value), &c); err != nil {
			continue
		}
		out = append(out, &c)
	}
	return out, nil
}

func (s *acmStore) deleteCert(ctx context.Context, arn string) error {
	_ = s.store.Delete(ctx, nsTags, arn)
	return s.store.Delete(ctx, nsCerts, arn)
}

func (s *acmStore) setTags(ctx context.Context, arn string, tags []Tag) error {
	raw, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	return s.store.Set(ctx, nsTags, arn, string(raw))
}

func (s *acmStore) getTags(ctx context.Context, arn string) ([]Tag, error) {
	raw, found, err := s.store.Get(ctx, nsTags, arn)
	if err != nil {
		return nil, err
	}
	if !found {
		return []Tag{}, nil
	}
	var tags []Tag
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		return nil, err
	}
	return tags, nil
}

// ─── Service ──────────────────────────────────────────────────

// Service implements router.Service and router.TargetDispatcher for ACM.
type Service struct {
	handler *Handler
	log     *serviceutil.ServiceLogger
	store   *acmStore
	cfg     *config.Config
	clk     clock.Clock
}

// New returns a configured ACM Service.
func New(cfg *config.Config, st state.Store, logger *zap.Logger, clk clock.Clock) *Service {
	store := newACMStore(st, cfg, clk)
	return &Service{
		log:     serviceutil.NewServiceLogger(logger, serviceName),
		handler: newHandler(cfg, store, clk),
		store:   store,
		cfg:     cfg,
		clk:     clk,
	}
}

func (s *Service) Name() string                { return serviceName }
func (s *Service) RegisterRoutes(_ chi.Router) {}
func (s *Service) TargetPrefix() string        { return "CertificateManager." }

func (s *Service) Dispatch(w http.ResponseWriter, r *http.Request) {
	if c, opName := codec.FromContext(r.Context()); c != nil && opName != "" {
		if !codec.Supports(s.SupportedProtocols(), c) {
			w.Header().Set("x-emulator-unsupported-protocol", c.Name())
			c.WriteError(w, r, &protocol.AWSError{
				Code:       "UnsupportedProtocol",
				Message:    "ACM does not support wire protocol " + c.Name() + ".",
				HTTPStatus: http.StatusUnsupportedMediaType,
			})
			return
		}
		if c.Name() != codec.NameRPCv2CBOR {
			if fn, ok := s.handler.ops[opName]; ok {
				fn(w, r)
				return
			}
		}
		if typed, ok := s.handler.typedOp[opName]; ok {
			typed.Invoke(w, r, c)
			return
		}
		// A real, documented ACM operation Overcast has not implemented gets an
		// honest 501 rather than UnknownOperationException, which would wrongly
		// claim AWS has no such operation; a name AWS does not model keeps the
		// 400. #1656 made this split inline here; it is now the one helper the
		// other eleven JSON-tier services share (#1645), writing through the
		// request's own codec so an RPC v2 CBOR caller gets CBOR.
		serviceutil.WriteUnhandledOperation(w, r, c, awsapiService, opName, errUnknownOperation(opName))
		return
	}

	target := r.Header.Get("X-Amz-Target")
	op := target
	if idx := strings.LastIndex(target, "."); idx >= 0 {
		op = target[idx+1:]
	}
	if fn, ok := s.handler.ops[op]; ok {
		fn(w, r)
		return
	}
	// The same split on the no-codec fallback. This path served a blanket 501
	// for every name, so a target AWS does not model moves to the 400 the
	// codec arm has always answered it with. JSON11 writes what
	// protocol.NotImplementedJSON did: both go through protocol.WriteJSONError.
	serviceutil.WriteUnhandledOperation(w, r, codec.JSON11, awsapiService, op, errUnknownOperation(op))
}

// errUnknownOperation is ACM's answer for a name the AWS models do not carry.
func errUnknownOperation(op string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "UnknownOperationException",
		Message:    "Unknown ACM operation: " + op,
		HTTPStatus: http.StatusBadRequest,
	}
}

// mergeTags returns existing plus overrides, with overrides winning on key
// collisions. The result is Key-sorted: Go randomizes map iteration per
// process, so without this a certificate's Tags array would reorder between
// otherwise identical ListTagsForCertificate responses.
func mergeTags(existing, overrides []Tag) []Tag {
	m := make(map[string]string, len(existing)+len(overrides))
	for _, t := range existing {
		m[t.Key] = t.Value
	}
	for _, t := range overrides {
		m[t.Key] = t.Value
	}
	out := make([]Tag, 0, len(m))
	for k, v := range m {
		out = append(out, Tag{Key: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// acmTagCfg tunes the shared tag validator to ACM's error shape (#1052).
// ACM's tag operations model InvalidParameterException for a malformed
// request and no dedicated tag-count exception, so the 50-tag limit is
// reported the same way an invalid key or value is.
var acmTagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    "InvalidParameterException",
	InvalidCode:     "InvalidParameterException",
	ExceededMessage: "You have exceeded the maximum number of tags for a certificate.",
}

// validateCertTags converts ACM's [{Key,Value}] tag shape to the map the
// shared validator works in. Callers pass the MERGED set (existing plus
// incoming), not just the incoming delta, so the 50-tag limit is enforced
// across repeated AddTagsToCertificate/TagResource calls rather than only
// against whatever one call happens to add.
func validateCertTags(tags []Tag) *protocol.AWSError {
	m := make(map[string]string, len(tags))
	for _, t := range tags {
		m[t.Key] = t.Value
	}
	return serviceutil.ValidateTags(acmTagCfg, m)
}

func removeTagKeys(existing []Tag, keys []string) []Tag {
	if len(keys) == 0 {
		return existing
	}
	remove := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		remove[k] = struct{}{}
	}
	kept := make([]Tag, 0, len(existing))
	for _, t := range existing {
		if _, drop := remove[t.Key]; !drop {
			kept = append(kept, t)
		}
	}
	return kept
}
