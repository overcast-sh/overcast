package eventbridge

import (
	"context"
	"net/http"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ebTagCfg tunes shared tag validation to EventBridge's error shape.
var ebTagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    "ValidationException",
	InvalidCode:     "ValidationException",
	ExceededMessage: "Exceeded maximum number of tags allowed.",
}

func errEBResourceNotFound(arn string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ResourceNotFoundException",
		Message:    "Resource " + arn + " does not exist.",
		HTTPStatus: http.StatusBadRequest,
	}
}

// tagStore is the namespace EventBridge resource tags live in, keyed by the
// canonical ARN of the rule or bus they belong to.
func (s *Service) tagStore() *serviceutil.NSStore {
	return &serviceutil.NSStore{Store: s.store, NS: nsTags}
}

// ruleARN is the ARN Overcast mints for a rule, and the key its tags are held
// under. The bus segment is always present, as it is in the ARN every read path
// returns to a caller.
func (s *Service) ruleARN(ctx context.Context, busName, name string) string {
	return protocol.ARN(s.region(ctx), s.cfg.AccountID, "events", "rule/"+busName+"/"+name)
}

// busARN is the ARN Overcast mints for an event bus, and the key its tags are
// held under.
func (s *Service) busARN(ctx context.Context, name string) string {
	return protocol.ARN(s.region(ctx), s.cfg.AccountID, "events", "event-bus/"+name)
}

// requireTaggableResource checks that the ARN names a rule or event bus that
// actually exists, and returns the canonical ARN its tags are keyed by. Real
// EventBridge refuses tag operations on unknown resources instead of minting a
// tag store for any string.
//
// Canonicalising matters because a default-bus rule has two legal spellings —
// with and without the bus segment — and tagging through one of them must not
// hide the tags from the other, or from the delete path that has only the rule's
// name to work with.
func (s *Service) requireTaggableResource(ctx context.Context, arn string) (string, *protocol.AWSError) {
	// arn:aws:events:<region>:<account>:<resource>
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[2] != "events" {
		return "", &protocol.AWSError{
			Code:       "ValidationException",
			Message:    "Parameter ResourceARN is not a valid ARN.",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	resource := parts[5]
	switch {
	case strings.HasPrefix(resource, "rule/"):
		// Rules are stored under "<busName>/<ruleName>"; a default-bus rule
		// ARN may omit the bus segment.
		key := strings.TrimPrefix(resource, "rule/")
		busName, ruleName, ok := strings.Cut(key, "/")
		if !ok {
			busName, ruleName = "default", key
		}
		if _, found, err := s.store.Get(ctx, nsRules, serviceutil.RegionKey(s.region(ctx), busName+"/"+ruleName)); err != nil {
			return "", protocol.ErrInternalError
		} else if !found {
			return "", errEBResourceNotFound(arn)
		}
		return s.ruleARN(ctx, busName, ruleName), nil
	case strings.HasPrefix(resource, "event-bus/"):
		name := strings.TrimPrefix(resource, "event-bus/")
		if name == "default" {
			return s.busARN(ctx, name), nil
		}
		if _, found, err := s.store.Get(ctx, nsBuses, serviceutil.RegionKey(s.region(ctx), name)); err != nil {
			return "", protocol.ErrInternalError
		} else if !found {
			return "", errEBResourceNotFound(arn)
		}
		return s.busARN(ctx, name), nil
	default:
		return "", errEBResourceNotFound(arn)
	}
}

// applyResourceTags validates and merges tags into the resource's tag blob.
// Shared by the legacy JSON and typed CBOR paths so both stay in lockstep.
func (s *Service) applyResourceTags(ctx context.Context, arn string, incoming map[string]string) *protocol.AWSError {
	key, aerr := s.requireTaggableResource(ctx, arn)
	if aerr != nil {
		return aerr
	}
	_, aerr = serviceutil.ApplyStoreTags(ctx, s.tagStore(), key, incoming, ebTagCfg)
	return aerr
}

// removeResourceTags removes the keys from the resource's tag blob.
func (s *Service) removeResourceTags(ctx context.Context, arn string, keys []string) *protocol.AWSError {
	key, aerr := s.requireTaggableResource(ctx, arn)
	if aerr != nil {
		return aerr
	}
	_, aerr = serviceutil.RemoveStoreTags(ctx, s.tagStore(), key, keys)
	return aerr
}

// listResourceTags returns the resource's current tags.
func (s *Service) listResourceTags(ctx context.Context, arn string) (map[string]string, *protocol.AWSError) {
	key, aerr := s.requireTaggableResource(ctx, arn)
	if aerr != nil {
		return nil, aerr
	}
	return s.tagStore().Load(ctx, key)
}

// ── Deleting ─────────────────────────────────────────────────────────────────

// deleteRuleRecord removes a rule and everything keyed to it, and returns the
// rule's ARN for the event the caller publishes.
//
// DeleteRule was already careful to take the rule's targets and its fire
// bookkeeping with it, and left its tags behind — so ListTagsForResource kept
// answering for a rule that no longer existed. The two request paths, JSON and
// typed, each had their own copy of the list to forget a namespace from; there
// is one now.
func (s *Service) deleteRuleRecord(ctx context.Context, busName, name string) (string, *protocol.AWSError) {
	if busName == "" {
		busName = "default"
	}
	arn := s.ruleARN(ctx, busName, name)
	if aerr := s.tagStore().Delete(ctx, arn); aerr != nil {
		return "", aerr
	}
	key := serviceutil.RegionKey(s.region(ctx), busName+"/"+name)
	for _, ns := range []string{nsRules, nsTargets, nsLastFire, nsNextFire} {
		if err := s.store.Delete(ctx, ns, key); err != nil {
			return "", protocol.Wrap(protocol.ErrInternalError, err)
		}
	}
	return arn, nil
}

// deleteEventBusRecord removes an event bus, its tags and its permission
// policy, and returns the bus's ARN for the event the caller publishes.
func (s *Service) deleteEventBusRecord(ctx context.Context, name string) (string, *protocol.AWSError) {
	arn := s.busARN(ctx, name)
	if aerr := s.tagStore().Delete(ctx, arn); aerr != nil {
		return "", aerr
	}
	if aerr := s.deleteBusPolicyStatements(ctx, name); aerr != nil {
		return "", aerr
	}
	if err := s.store.Delete(ctx, nsBuses, serviceutil.RegionKey(s.region(ctx), name)); err != nil {
		return "", protocol.Wrap(protocol.ErrInternalError, err)
	}
	return arn, nil
}
