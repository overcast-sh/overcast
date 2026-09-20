package secretsmanager

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// Handler holds Secrets Manager handler dependencies.
type Handler struct {
	cfg   *config.Config
	store *smStore
	log   *serviceutil.ServiceLogger
	clk   clock.Clock
	bus   *events.Bus
	ops   map[string]http.HandlerFunc

	typedOp map[string]op.Operation

	// invoker is the synchronous Lambda seam the rotation protocol drives.
	// nil until InitLambdaInvoker runs, which is why every rotation path
	// reports an honest failure rather than assuming it is present.
	invoker events.FunctionSyncInvoker

	// rotationMu serialises rotation sequences so a scheduled sweep and an API
	// call never drive two protocols over one secret at once.
	rotationMu sync.Mutex

	// rotationWake nudges the background engine when a schedule changes.
	// Buffered depth 1: a pending wake already says everything a second would.
	rotationWake chan struct{}
}

// newHandler builds a Handler from the raw dependencies.
func newHandler(cfg *config.Config, store state.Store, log *serviceutil.ServiceLogger, clk clock.Clock) *Handler {
	h := &Handler{
		cfg:          cfg,
		store:        newSMStore(store, clk, cfg.Region),
		log:          log,
		clk:          clk,
		rotationWake: make(chan struct{}, 1),
	}
	h.initOps()
	return h
}

// InitLambdaInvoker wires the synchronous Lambda invoker used by rotation.
func (h *Handler) InitLambdaInvoker(inv events.FunctionSyncInvoker) { h.invoker = inv }

// jsonOp adapts a typed operation function to the AWS JSON 1.0/1.1 dispatch
// path, which is what X-Amz-Target requests take.
//
// Both wire protocols run the same function deliberately. Secrets Manager used
// to carry two implementations of every operation — one here, one in
// typed_logic.go for CBOR — and rotation's staging-label rules are exactly the
// kind of behaviour that would have drifted between them.
func jsonOp[In any, Out any](fn func(context.Context, *In) (*Out, *protocol.AWSError)) http.HandlerFunc {
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
		protocol.WriteJSON(w, r, http.StatusOK, out)
	}
}

// initOps registers every known Secrets Manager operation to its handler.
func (h *Handler) initOps() {
	h.ops = map[string]http.HandlerFunc{
		// Secret CRUD
		"CreateSecret":         jsonOp(h.createSecretTyped),
		"GetSecretValue":       jsonOp(h.getSecretValueTyped),
		"DescribeSecret":       jsonOp(h.describeSecretTyped),
		"PutSecretValue":       jsonOp(h.putSecretValueTyped),
		"UpdateSecret":         jsonOp(h.updateSecretTyped),
		"ListSecrets":          jsonOp(h.listSecretsTyped),
		"ListSecretVersionIds": jsonOp(h.listSecretVersionIdsTyped),
		"DeleteSecret":         jsonOp(h.deleteSecretTyped),
		"BatchGetSecretValue":  jsonOp(h.batchGetSecretValueTyped),
		// Rotation
		"RotateSecret":             jsonOp(h.rotateSecretTyped),
		"CancelRotateSecret":       jsonOp(h.cancelRotateSecretTyped),
		"UpdateSecretVersionStage": jsonOp(h.updateSecretVersionStageTyped),
		// Tags
		"TagResource":   jsonOp(h.tagResourceTyped),
		"UntagResource": jsonOp(h.untagResourceTyped),
		// Password
		"GetRandomPassword": jsonOp(h.getRandomPasswordTyped),
		// Resource policies (stored and validated, never evaluated — see policy.go)
		"GetResourcePolicy":      jsonOp(h.getResourcePolicyTyped),
		"PutResourcePolicy":      jsonOp(h.putResourcePolicyTyped),
		"DeleteResourcePolicy":   jsonOp(h.deleteResourcePolicyTyped),
		"ValidateResourcePolicy": jsonOp(h.validateResourcePolicyTyped),
		// Deletion recovery
		"RestoreSecret": jsonOp(h.restoreSecretTyped),
		// Stubs (handler_stubs.go)
		"ReplicateSecretToRegions":     h.ReplicateSecretToRegions,
		"RemoveRegionsFromReplication": h.RemoveRegionsFromReplication,
	}
	h.typedOp = h.typedOps()
}

// ─── GetRandomPassword charset ─────────────────────────────────────────────

// defaultPasswordLength matches the AWS Secrets Manager default.
const defaultPasswordLength = 32

// defaultPasswordChars is the full default character set (upper, lower, digit, symbol).
const defaultPasswordChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"

// containsRune reports whether s contains r.
func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}

// ─── ListSecrets / BatchGetSecretValue filters ─────────────────────────────

// secretFilter is one entry of BatchGetSecretValue/ListSecrets Filters.
type secretFilter struct {
	Key    string   `json:"Key" cbor:"Key"`
	Values []string `json:"Values" cbor:"Values"`
}

// secretFilterKeys is AWS's complete FilterNameStringType enum — every Key
// value ListSecrets/BatchGetSecretValue's Filters accepts.
//
// "primary-region" and "owning-service" are accepted here but never match
// below: Overcast does not model secret replication or AWS-managed (e.g.
// RDS-owned) secrets, so no secret ever carries either attribute, and an
// empty match is the honest answer for an account with none of either — the
// same reasoning EC2's filterSpec uses for a real tag key nothing happens to
// have. That is a coverage gap, not a validation one, and out of scope here.
//
// https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_Filter.html
var secretFilterKeys = map[string]bool{
	"description": true, "name": true, "tag-key": true, "tag-value": true,
	"primary-region": true, "owning-service": true, "all": true,
}

// validateSecretFilters refuses a Filter.Key outside AWS's enum, before the
// store is ever scanned. Real Secrets Manager declares InvalidParameterException
// as one of ListSecrets' and BatchGetSecretValue's own errors for exactly
// this, so — unlike the two other findings in the cross-service filter-value
// survey — no new error had to be invented.
func validateSecretFilters(filters []secretFilter) *protocol.AWSError {
	for _, f := range filters {
		if !secretFilterKeys[strings.ToLower(f.Key)] {
			return errInvalidParameter(fmt.Sprintf(
				"1 validation error detected: Value '%s' at 'filters.1.member.key' failed to satisfy constraint: Member must satisfy enum value set: [description, name, tag-key, tag-value, primary-region, owning-service, all]",
				f.Key))
		}
	}
	return nil
}

// secretMatchesFilters reports whether a secret satisfies every filter, which
// is how AWS combines them: filters AND together, values within one filter OR
// together. Matching is a case-insensitive prefix test, which is what AWS
// documents for name and description.
func secretMatchesFilters(sec Secret, filters []secretFilter) bool {
	for _, f := range filters {
		if !secretMatchesFilter(sec, f.Key, f.Values) {
			return false
		}
	}
	return true
}

func secretMatchesFilter(sec Secret, key string, values []string) bool {
	matchAny := func(candidates ...string) bool {
		for _, v := range values {
			needle := strings.ToLower(strings.TrimPrefix(v, "!"))
			for _, c := range candidates {
				if strings.HasPrefix(strings.ToLower(c), needle) {
					return true
				}
			}
		}
		return false
	}
	switch strings.ToLower(key) {
	case "name":
		return matchAny(sec.Name)
	case "description":
		return matchAny(sec.Description)
	case "tag-key":
		keys := make([]string, 0, len(sec.Tags))
		for _, tag := range sec.Tags {
			keys = append(keys, tag.Key)
		}
		return matchAny(keys...)
	case "tag-value":
		vals := make([]string, 0, len(sec.Tags))
		for _, tag := range sec.Tags {
			vals = append(vals, tag.Value)
		}
		return matchAny(vals...)
	case "all":
		candidates := []string{sec.Name, sec.Description}
		for _, tag := range sec.Tags {
			candidates = append(candidates, tag.Key, tag.Value)
		}
		return matchAny(candidates...)
	default:
		// An unknown key matches nothing rather than everything: a filter the
		// emulator does not understand must not silently widen the result.
		return false
	}
}
