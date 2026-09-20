package secretsmanager

// policy.go — resource-based policy CRUD and validation.
//
// SCOPE, stated plainly because the difference matters: these four operations
// store, return, delete and *syntactically validate* a secret's resource
// policy. Nothing evaluates it. A policy that denies every principal does not
// stop a single GetSecretValue call in Overcast today, and neither does one
// that grants access to a principal with no identity policy.
//
// Request-time IAM enforcement (OVERCAST_ENFORCE_IAM, off by default) consults
// identity policies only; wiring every service's stored resource policy into
// that path is tracked separately in #496, and Secrets Manager is named there.
// Storing without enforcing is still strictly better than the 501 these
// operations used to return — a CDK/CloudFormation stack that attaches a
// secret policy now provisions and reads back — but a caller must not read
// "PutResourcePolicy succeeded" as "the policy is in force".

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ─── Wire shapes ───────────────────────────────────────────────────────────

type putResourcePolicyRequest struct {
	SecretId          string `json:"SecretId" cbor:"SecretId"`
	ResourcePolicy    string `json:"ResourcePolicy" cbor:"ResourcePolicy"`
	BlockPublicPolicy *bool  `json:"BlockPublicPolicy" cbor:"BlockPublicPolicy"`
}

type resourcePolicyIdentityResponse struct {
	ARN  string `json:"ARN" cbor:"ARN"`
	Name string `json:"Name" cbor:"Name"`
}

type getResourcePolicyResponse struct {
	ARN            string `json:"ARN" cbor:"ARN"`
	Name           string `json:"Name" cbor:"Name"`
	ResourcePolicy string `json:"ResourcePolicy,omitempty" cbor:"ResourcePolicy,omitempty"`
}

type validateResourcePolicyRequest struct {
	SecretId       string `json:"SecretId" cbor:"SecretId"`
	ResourcePolicy string `json:"ResourcePolicy" cbor:"ResourcePolicy"`
}

type validationErrorEntry struct {
	CheckName    string `json:"CheckName" cbor:"CheckName"`
	ErrorMessage string `json:"ErrorMessage" cbor:"ErrorMessage"`
}

type validateResourcePolicyResponse struct {
	PolicyValidationPassed bool                   `json:"PolicyValidationPassed" cbor:"PolicyValidationPassed"`
	ValidationErrors       []validationErrorEntry `json:"ValidationErrors" cbor:"ValidationErrors"`
}

// ─── Operations ────────────────────────────────────────────────────────────

func (h *Handler) putResourcePolicyTyped(ctx context.Context, req *putResourcePolicyRequest) (*resourcePolicyIdentityResponse, *protocol.AWSError) {
	if req.ResourcePolicy == "" {
		return nil, errInvalidParameter("You must provide a value for the ResourcePolicy parameter.")
	}
	sec, aerr := h.resolveLiveSecret(ctx, req.SecretId)
	if aerr != nil {
		return nil, aerr
	}

	doc, findings := parsePolicyDocument(req.ResourcePolicy)
	if len(findings) > 0 {
		return nil, errMalformedPolicy(findings[0].ErrorMessage)
	}
	if req.BlockPublicPolicy != nil && *req.BlockPublicPolicy && doc.grantsPublicAccess() {
		return nil, errMalformedPolicy(
			"Resource policy contains a statement granting access to all principals, and BlockPublicPolicy is set.")
	}

	sec.ResourcePolicy = req.ResourcePolicy
	sec.LastChangedDate = float64(h.store.now().Unix())
	if aerr := h.store.putSecret(ctx, sec); aerr != nil {
		return nil, aerr
	}
	return &resourcePolicyIdentityResponse{ARN: sec.ARN, Name: sec.Name}, nil
}

func (h *Handler) getResourcePolicyTyped(ctx context.Context, req *secretIDRequest) (*getResourcePolicyResponse, *protocol.AWSError) {
	sec, aerr := h.store.resolveSecret(ctx, req.SecretId)
	if aerr != nil {
		return nil, aerr
	}
	return &getResourcePolicyResponse{ARN: sec.ARN, Name: sec.Name, ResourcePolicy: sec.ResourcePolicy}, nil
}

func (h *Handler) deleteResourcePolicyTyped(ctx context.Context, req *secretIDRequest) (*resourcePolicyIdentityResponse, *protocol.AWSError) {
	sec, aerr := h.resolveLiveSecret(ctx, req.SecretId)
	if aerr != nil {
		return nil, aerr
	}
	// AWS is idempotent here: deleting an absent policy is a 200.
	sec.ResourcePolicy = ""
	sec.LastChangedDate = float64(h.store.now().Unix())
	if aerr := h.store.putSecret(ctx, sec); aerr != nil {
		return nil, aerr
	}
	return &resourcePolicyIdentityResponse{ARN: sec.ARN, Name: sec.Name}, nil
}

// validateResourcePolicyTyped answers 200 whether or not the policy is valid —
// the findings are the payload, not an error. That is AWS's shape, and it is
// what makes the operation useful in a "check before you put" flow.
func (h *Handler) validateResourcePolicyTyped(ctx context.Context, req *validateResourcePolicyRequest) (*validateResourcePolicyResponse, *protocol.AWSError) {
	if req.ResourcePolicy == "" {
		return nil, errInvalidParameter("You must provide a value for the ResourcePolicy parameter.")
	}
	// SecretId is optional on AWS; when given it must exist.
	if req.SecretId != "" {
		if _, aerr := h.store.resolveSecret(ctx, req.SecretId); aerr != nil {
			return nil, aerr
		}
	}

	_, findings := parsePolicyDocument(req.ResourcePolicy)
	if findings == nil {
		findings = []validationErrorEntry{}
	}
	return &validateResourcePolicyResponse{
		PolicyValidationPassed: len(findings) == 0,
		ValidationErrors:       findings,
	}, nil
}

// ─── Document parsing ──────────────────────────────────────────────────────

// policyDocument is the subset of the IAM policy grammar these operations need
// in order to answer "is this a policy at all" and "does it name every
// principal". It deliberately stops well short of an evaluator: Overcast has
// one shared policy evaluator and this is not a second one.
type policyDocument struct {
	Version   string        `json:"Version"`
	ID        string        `json:"Id"`
	Statement statementList `json:"Statement"`
}

// statementList accepts both spellings of Statement the IAM grammar allows: an
// array of statements, and a single bare statement object. Rejecting the
// second would fail a policy AWS accepts, which is exactly the kind of
// false negative that makes a validator worse than none.
type statementList []policyStatement

func (l *statementList) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var one policyStatement
		if err := json.Unmarshal(trimmed, &one); err != nil {
			return err
		}
		*l = statementList{one}
		return nil
	}
	var many []policyStatement
	if err := json.Unmarshal(trimmed, &many); err != nil {
		return err
	}
	*l = many
	return nil
}

type policyStatement struct {
	Sid       string `json:"Sid"`
	Effect    string `json:"Effect"`
	Principal any    `json:"Principal"`
	Action    any    `json:"Action"`
	Resource  any    `json:"Resource"`
	Condition any    `json:"Condition"`
}

// Check names reported alongside each finding. AWS does not publish the full
// set its own validator uses, so these are Overcast's, in AWS's shape — a
// caller should read the ErrorMessage, and treat CheckName as a coarse
// category rather than a stable identifier to branch on.
const (
	checkSyntax = "SYNTAX_ERROR"
	checkSchema = "POLICY_SCHEMA_ERROR"
)

// parsePolicyDocument returns the decoded document plus any validation
// findings. A non-empty findings slice means the document is not usable.
func parsePolicyDocument(raw string) (*policyDocument, []validationErrorEntry) {
	var doc policyDocument
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		// A JSON array or scalar decodes into the struct as a type error, which
		// is the same class of problem as malformed JSON.
		return nil, []validationErrorEntry{{
			CheckName:    checkSyntax,
			ErrorMessage: "The policy document is not valid JSON: " + err.Error(),
		}}
	}

	var findings []validationErrorEntry
	if doc.Version == "" {
		findings = append(findings, validationErrorEntry{
			CheckName:    checkSchema,
			ErrorMessage: "The policy document must include a Version element.",
		})
	}
	if len(doc.Statement) == 0 {
		findings = append(findings, validationErrorEntry{
			CheckName:    checkSchema,
			ErrorMessage: "The policy document must include at least one Statement.",
		})
	}
	for i, st := range doc.Statement {
		switch st.Effect {
		case "Allow", "Deny":
		case "":
			findings = append(findings, validationErrorEntry{
				CheckName:    checkSchema,
				ErrorMessage: fmt.Sprintf("Statement %d is missing the required Effect element.", i+1),
			})
		default:
			findings = append(findings, validationErrorEntry{
				CheckName:    checkSchema,
				ErrorMessage: fmt.Sprintf("Statement %d has Effect %q; it must be Allow or Deny.", i+1, st.Effect),
			})
		}
		if st.Action == nil {
			findings = append(findings, validationErrorEntry{
				CheckName:    checkSchema,
				ErrorMessage: fmt.Sprintf("Statement %d is missing the required Action element.", i+1),
			})
		}
		if st.Principal == nil {
			findings = append(findings, validationErrorEntry{
				CheckName:    checkSchema,
				ErrorMessage: fmt.Sprintf("Statement %d is missing the required Principal element for a resource policy.", i+1),
			})
		}
	}
	return &doc, findings
}

// grantsPublicAccess reports whether any Allow statement names every principal
// without narrowing it by a Condition — what BlockPublicPolicy refuses. The
// check itself is shared with Lambda's PutResourcePolicy in serviceutil.
func (d *policyDocument) grantsPublicAccess() bool {
	if d == nil {
		return false
	}
	for _, st := range d.Statement {
		if serviceutil.StatementGrantsPublicAccess(st.Effect, st.Principal, st.Condition) {
			return true
		}
	}
	return false
}
