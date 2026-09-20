package acm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

type requestCertificateRequest struct {
	DomainName              string   `json:"DomainName"`
	ValidationMethod        string   `json:"ValidationMethod"`
	SubjectAlternativeNames []string `json:"SubjectAlternativeNames"`
	Tags                    []Tag    `json:"Tags"`
}

type requestCertificateResponse struct {
	CertificateArn string `json:"CertificateArn"`
}

type describeCertificateRequest struct {
	CertificateArn string `json:"CertificateArn"`
}

type describeCertificateResponse struct {
	Certificate *Certificate `json:"Certificate"`
}

// listCertificatesRequest carries the one filter Overcast can answer from
// what it stores. CertificateKeyPairOrigins and Includes (keyTypes,
// keyUsage, extendedKeyUsage, exportOption, managedBy) select on certificate
// material and export configuration that Overcast never generates, so there
// is nothing here to filter on; MaxItems/NextToken and SortBy/SortOrder are
// not implemented either. All of them are absent rather than declared-and-
// ignored so that this struct says exactly what is honoured. See
// docs/services/acm.md "Differences from AWS".
type listCertificatesRequest struct {
	CertificateStatuses []string `json:"CertificateStatuses"`
}

type listCertificatesResponse struct {
	CertificateSummaryList []certificateSummaryWire `json:"CertificateSummaryList"`
}

type certificateSummaryWire struct {
	CertificateArn string `json:"CertificateArn"`
	DomainName     string `json:"DomainName"`
	Status         string `json:"Status"`
	Type           string `json:"Type"`
}

type listCertificateDomainValidationsRequest struct {
	CertificateArn string `json:"CertificateArn"`
	MaxItems       int    `json:"MaxItems"`
	NextToken      string `json:"NextToken"`
}

type listCertificateDomainValidationsResponse struct {
	DomainValidationSummaryList []domainValidationSummaryWire `json:"DomainValidationSummaryList"`
	NextToken                   string                        `json:"NextToken,omitempty"`
}

// domainValidationSummaryWire mirrors ACM's DomainValidationSummary shape —
// added alongside ACM's email-to-DNS validation-method migration feature
// (https://aws.amazon.com/about-aws/whats-new/2026/08/AWS-Certificate-Manager-Email-DNS-Switch/),
// verified against
// https://docs.aws.amazon.com/acm/latest/APIReference/API_DomainValidationSummary.html.
// RequestedValidationConfiguration is only ever populated by real AWS while a
// validation-method migration is in progress; Overcast never starts one, so
// it is always omitted here.
type domainValidationSummaryWire struct {
	DomainName                       string                       `json:"DomainName"`
	ActiveValidationConfiguration    *validationConfigurationWire `json:"ActiveValidationConfiguration,omitempty"`
	RequestedValidationConfiguration *validationConfigurationWire `json:"RequestedValidationConfiguration,omitempty"`
}

// validationConfigurationWire mirrors ACM's ValidationConfiguration shape
// (https://docs.aws.amazon.com/acm/latest/APIReference/API_ValidationConfiguration.html).
// ValidationMethod echoes the method RequestCertificate recorded for that
// domain, which is the same value DescribeCertificate reports for it.
//
// ValidationChallenge — the nested shape carrying the DNS record or the
// validation email addresses — is still never populated. DescribeCertificate's
// DomainValidationOptions is where a caller reads the CNAME to publish
// (#1994), and duplicating it into a second, differently-shaped member that
// AWS fills only during a validation-method migration would be a divergence
// real AWS never produces.
type validationConfigurationWire struct {
	ValidationMethod string `json:"ValidationMethod,omitempty"`
	ValidationStatus string `json:"ValidationStatus,omitempty"`
}

type deleteCertificateRequest struct {
	CertificateArn string `json:"CertificateArn"`
}

type listTagsForCertificateRequest struct {
	CertificateArn string `json:"CertificateArn"`
}

type listTagsForCertificateResponse struct {
	Tags []Tag `json:"Tags"`
}

type addTagsToCertificateRequest struct {
	CertificateArn string `json:"CertificateArn"`
	Tags           []Tag  `json:"Tags"`
}

type removeTagsFromCertificateRequest struct {
	CertificateArn string `json:"CertificateArn"`
	Tags           []Tag  `json:"Tags"`
}

// The modern aliases. ACM added TagResource / UntagResource /
// ListTagsForResource alongside the *Certificate spellings; both address the
// same tag set on the same certificate.
//
// AWS spells the resource `ResourceArn` here rather than reusing
// `CertificateArn`: the pinned manifest carries operation names and HTTP
// bindings but not member shapes, so this follows the universal convention for
// operations named TagResource/UntagResource. See
// docs/plans/resource-tagging-coverage.md.

type tagResourceRequest struct {
	ResourceArn string `json:"ResourceArn"`
	Tags        []Tag  `json:"Tags"`
}

type untagResourceRequest struct {
	ResourceArn string   `json:"ResourceArn"`
	TagKeys     []string `json:"TagKeys"`
}

type listTagsForResourceRequest struct {
	ResourceArn string `json:"ResourceArn"`
}

type listTagsForResourceResponse struct {
	Tags []Tag `json:"Tags"`
}

func (h *Handler) requestCertificateTyped(ctx context.Context, req *requestCertificateRequest) (*requestCertificateResponse, *protocol.AWSError) {
	if req.DomainName == "" {
		return nil, &protocol.AWSError{
			Code: "InvalidParameterException", Message: "DomainName is required",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	// Shape constraints before anything is created, so a rejected request
	// strands no certificate — the same ordering the inline-tag check below
	// keeps (#1052). DomainName and every SAN target DomainNameString, so one
	// validator covers both; the member path is what tells the caller which
	// of the two was wrong (#1994).
	if aerr := validateDomainName(req.DomainName, "domainName"); aerr != nil {
		return nil, aerr
	}
	for i, san := range req.SubjectAlternativeNames {
		if aerr := validateDomainName(san, fmt.Sprintf("subjectAlternativeNames.%d.member", i+1)); aerr != nil {
			return nil, aerr
		}
	}
	method := req.ValidationMethod
	if method == "" {
		// ACM validates by email when RequestCertificate names no method.
		method = "EMAIL"
	} else if aerr := validateEnum(method, "validationMethod", validationMethods); aerr != nil {
		return nil, aerr
	}
	region := middleware.RegionFromContext(ctx, h.cfg.Region)
	certID := uuid.NewString()
	arn := fmt.Sprintf("arn:aws:acm:%s:%s:certificate/%s", region, h.cfg.AccountID, certID)
	now := float64(h.clk.Now().Unix())

	sans := req.SubjectAlternativeNames
	if len(sans) == 0 {
		sans = []string{req.DomainName}
	}

	// Tag validation is a request-shape constraint, checked before the
	// certificate is created — the same ordering createLogGroupTyped uses
	// (internal/services/cloudwatch/logs/typed_logic.go) and for the same
	// reason: a rejected request must not leave a certificate behind with no
	// tags reachable to fix (#1052).
	merged := mergeTags(nil, req.Tags)
	if aerr := validateCertTags(merged); aerr != nil {
		return nil, aerr
	}

	cert := &Certificate{
		CertificateArn:          arn,
		DomainName:              req.DomainName,
		SubjectAlternativeNames: sans,
		Status:                  "ISSUED",
		Type:                    "AMAZON_ISSUED",
		CreatedAt:               now,
		IssuedAt:                now,
	}
	// AWS builds DomainValidationOptions as a result of the RequestCertificate
	// request itself, so they are stored with the certificate rather than
	// synthesized per DescribeCertificate call — which is also what keeps a
	// validation record stable across calls.
	cert.DomainValidationOptions = domainValidations(arn, certDomains(cert), method)
	if err := h.store.putCert(ctx, cert); err != nil {
		return nil, protocol.ErrInternalError
	}
	// AWS applies RequestCertificate's Tags when the certificate is issued,
	// and authorizes that separately from AddTagsToCertificate.
	if len(req.Tags) > 0 {
		if err := h.store.setTags(ctx, arn, merged); err != nil {
			return nil, protocol.ErrInternalError
		}
	}
	return &requestCertificateResponse{CertificateArn: arn}, nil
}

func (h *Handler) describeCertificateTyped(ctx context.Context, req *describeCertificateRequest) (*describeCertificateResponse, *protocol.AWSError) {
	cert, found := h.store.getCert(ctx, req.CertificateArn)
	if !found {
		return nil, errCertificateNotFound(req.CertificateArn)
	}
	return &describeCertificateResponse{Certificate: cert}, nil
}

func (h *Handler) listCertificatesTyped(ctx context.Context, req *listCertificatesRequest) (*listCertificatesResponse, *protocol.AWSError) {
	// A status outside the enum is rejected rather than ignored: a typo'd
	// filter that silently matched everything is the failure this whole
	// finding is about (#1994).
	wanted := make(map[string]struct{}, len(req.CertificateStatuses))
	for i, status := range req.CertificateStatuses {
		member := fmt.Sprintf("certificateStatuses.%d.member", i+1)
		if aerr := validateEnum(status, member, certificateStatuses); aerr != nil {
			return nil, aerr
		}
		wanted[status] = struct{}{}
	}

	certs, err := h.store.listCerts(ctx)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	summaries := make([]certificateSummaryWire, 0, len(certs))
	for _, c := range certs {
		if len(wanted) > 0 {
			if _, ok := wanted[c.Status]; !ok {
				continue
			}
		}
		summaries = append(summaries, certificateSummaryWire{
			CertificateArn: c.CertificateArn,
			DomainName:     c.DomainName,
			Status:         c.Status,
			Type:           c.Type,
		})
	}
	return &listCertificatesResponse{CertificateSummaryList: summaries}, nil
}

// domainValidationsPageOptions mirrors AWS's documented default and cap for
// ListCertificateDomainValidations' MaxItems: default 1000, no larger than
// 1000 (https://docs.aws.amazon.com/acm/latest/APIReference/API_ListCertificateDomainValidations.html).
var domainValidationsPageOptions = serviceutil.PaginateOptions{DefaultLimit: 1000, MaxLimit: 1000}

func (h *Handler) listCertificateDomainValidationsTyped(ctx context.Context, req *listCertificateDomainValidationsRequest) (*listCertificateDomainValidationsResponse, *protocol.AWSError) {
	cert, aerr := h.lookupCert(ctx, req.CertificateArn)
	if aerr != nil {
		return nil, aerr
	}

	domains := certDomains(cert)
	page, err := serviceutil.Paginate(domains, req.MaxItems, req.NextToken, domainValidationsPageOptions)
	if err != nil {
		return nil, &protocol.AWSError{
			Code:       "InvalidArgsException",
			Message:    "The specified next token is invalid.",
			HTTPStatus: http.StatusBadRequest,
		}
	}

	methods := make(map[string]string, len(cert.DomainValidationOptions))
	for _, opt := range cert.DomainValidationOptions {
		methods[opt.DomainName] = opt.ValidationMethod
	}
	summaries := make([]domainValidationSummaryWire, 0, len(page.Items))
	for _, domain := range page.Items {
		summaries = append(summaries, domainValidationSummaryWire{
			DomainName: domain,
			// Overcast never performs real DNS/email validation — every
			// certificate is ISSUED immediately (RequestCertificate), so
			// every domain it names is honestly reported as already
			// validated, consistent with that fiction.
			ActiveValidationConfiguration: &validationConfigurationWire{
				ValidationMethod: methods[domain],
				ValidationStatus: "SUCCESS",
			},
		})
	}
	return &listCertificateDomainValidationsResponse{
		DomainValidationSummaryList: summaries,
		NextToken:                   page.NextToken,
	}, nil
}

// certDomains returns the certificate's DomainName plus each
// SubjectAlternativeNames entry, de-duplicated in encounter order — one
// entry per unique domain actually on the certificate. Real AWS's own
// DescribeCertificate.DomainValidationOptions works the same way: one entry
// per SAN, and the primary domain name is always among the SANs.
func certDomains(cert *Certificate) []string {
	domains := make([]string, 0, 1+len(cert.SubjectAlternativeNames))
	seen := make(map[string]struct{}, cap(domains))
	add := func(d string) {
		if _, ok := seen[d]; ok {
			return
		}
		seen[d] = struct{}{}
		domains = append(domains, d)
	}
	add(cert.DomainName)
	for _, san := range cert.SubjectAlternativeNames {
		add(san)
	}
	return domains
}

// domainValidations builds one DomainValidation per domain on the certificate,
// the way AWS populates CertificateDetail.DomainValidationOptions from the
// RequestCertificate request.
//
// ValidationStatus is SUCCESS for every domain because the certificate is
// ISSUED on return — a PENDING_VALIDATION entry on an ISSUED certificate is a
// state AWS never reports, and callers branch on it.
//
// The DNS branch is the one place Overcast does synthesize challenge data.
// The CNAME is not decoration: `aws_acm_certificate_validation`, the CDK's
// DnsValidatedCertificate and every "publish the record, then wait" script
// read ResourceRecord and act on it, so an absent record stops them dead,
// while a present one lets the whole flow run against a certificate that is
// already valid. EMAIL reports the domain the mail would have gone to and
// HTTP reports neither, since its HttpRedirect analogue exists only for
// CloudFront-issued certificates.
func domainValidations(arn string, domains []string, method string) []DomainValidation {
	out := make([]DomainValidation, 0, len(domains))
	for _, domain := range domains {
		entry := DomainValidation{
			DomainName:       domain,
			ValidationStatus: "SUCCESS",
			ValidationMethod: method,
		}
		switch method {
		case "DNS":
			entry.ResourceRecord = &ResourceRecord{
				Name:  "_" + validationToken(arn, domain, "name") + "." + domain + ".",
				Type:  "CNAME",
				Value: "_" + validationToken(arn, domain, "value") + ".acm-validations.aws.",
			}
		case "EMAIL":
			entry.ValidationDomain = domain
		}
		out = append(out, entry)
	}
	return out
}

// validationToken derives the 32 hex characters AWS uses for each half of a
// DNS validation record. It is a hash of the certificate ARN and the domain
// rather than anything random or time-based, so the record a caller is told to
// publish is the same one every later DescribeCertificate reports — including
// after a restart, since the options are stored with the certificate.
func validationToken(arn, domain, part string) string {
	sum := sha256.Sum256([]byte(arn + "|" + domain + "|" + part))
	return hex.EncodeToString(sum[:16])
}

func (h *Handler) deleteCertificateTyped(ctx context.Context, req *deleteCertificateRequest) (*struct{}, *protocol.AWSError) {
	if _, found := h.store.getCert(ctx, req.CertificateArn); !found {
		return nil, errCertificateNotFound(req.CertificateArn)
	}
	if err := h.store.deleteCert(ctx, req.CertificateArn); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &struct{}{}, nil
}

func (h *Handler) listTagsForCertificateTyped(ctx context.Context, req *listTagsForCertificateRequest) (*listTagsForCertificateResponse, *protocol.AWSError) {
	if aerr := h.requireCert(ctx, req.CertificateArn); aerr != nil {
		return nil, aerr
	}
	tags, err := h.store.getTags(ctx, req.CertificateArn)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	return &listTagsForCertificateResponse{Tags: tags}, nil
}

func (h *Handler) addTagsToCertificateTyped(ctx context.Context, req *addTagsToCertificateRequest) (*struct{}, *protocol.AWSError) {
	if aerr := h.requireCert(ctx, req.CertificateArn); aerr != nil {
		return nil, aerr
	}
	existing, err := h.store.getTags(ctx, req.CertificateArn)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	merged := mergeTags(existing, req.Tags)
	if aerr := validateCertTags(merged); aerr != nil {
		return nil, aerr
	}
	if err := h.store.setTags(ctx, req.CertificateArn, merged); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &struct{}{}, nil
}

func (h *Handler) removeTagsFromCertificateTyped(ctx context.Context, req *removeTagsFromCertificateRequest) (*struct{}, *protocol.AWSError) {
	if aerr := h.requireCert(ctx, req.CertificateArn); aerr != nil {
		return nil, aerr
	}
	existing, err := h.store.getTags(ctx, req.CertificateArn)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	keys := make([]string, 0, len(req.Tags))
	for _, t := range req.Tags {
		keys = append(keys, t.Key)
	}
	if err := h.store.setTags(ctx, req.CertificateArn, removeTagKeys(existing, keys)); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &struct{}{}, nil
}

// lookupCert validates arn and looks up the certificate it names, in a
// single store read. It is the shared implementation behind requireCert and
// any caller that also needs the certificate itself (e.g.
// listCertificateDomainValidationsTyped), so there is exactly one read and
// one not-found error built per lookup instead of a validate-then-fetch pair
// that could race with a concurrent DeleteCertificate.
func (h *Handler) lookupCert(ctx context.Context, arn string) (*Certificate, *protocol.AWSError) {
	if arn == "" {
		return nil, &protocol.AWSError{
			Code: "InvalidArnException", Message: "CertificateArn is required", HTTPStatus: http.StatusBadRequest,
		}
	}
	cert, found := h.store.getCert(ctx, arn)
	if !found {
		return nil, errCertificateNotFound(arn)
	}
	return cert, nil
}

// requireCert reports a missing certificate rather than letting a tag write
// land under an ARN nothing owns — DeleteCertificate is the only thing that
// clears the tag namespace, so an orphaned entry would never be collected.
func (h *Handler) requireCert(ctx context.Context, arn string) *protocol.AWSError {
	_, aerr := h.lookupCert(ctx, arn)
	return aerr
}

// errCertificateNotFound builds the ResourceNotFoundException ACM returns
// when CertificateArn doesn't name a certificate that exists.
func errCertificateNotFound(arn string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ResourceNotFoundException",
		Message:    fmt.Sprintf("Certificate %s not found", arn),
		HTTPStatus: http.StatusBadRequest,
	}
}

func (h *Handler) tagResourceTyped(ctx context.Context, req *tagResourceRequest) (*struct{}, *protocol.AWSError) {
	return h.addTagsToCertificateTyped(ctx, &addTagsToCertificateRequest{
		CertificateArn: req.ResourceArn, Tags: req.Tags,
	})
}

// untagResourceTyped takes TagKeys where RemoveTagsFromCertificate takes a
// Tags list, so it cannot simply delegate — the keys become the removal set.
func (h *Handler) untagResourceTyped(ctx context.Context, req *untagResourceRequest) (*struct{}, *protocol.AWSError) {
	if aerr := h.requireCert(ctx, req.ResourceArn); aerr != nil {
		return nil, aerr
	}
	existing, err := h.store.getTags(ctx, req.ResourceArn)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if err := h.store.setTags(ctx, req.ResourceArn, removeTagKeys(existing, req.TagKeys)); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &struct{}{}, nil
}

func (h *Handler) listTagsForResourceTyped(ctx context.Context, req *listTagsForResourceRequest) (*listTagsForResourceResponse, *protocol.AWSError) {
	out, aerr := h.listTagsForCertificateTyped(ctx, &listTagsForCertificateRequest{CertificateArn: req.ResourceArn})
	if aerr != nil {
		return nil, aerr
	}
	return &listTagsForResourceResponse{Tags: out.Tags}, nil
}
