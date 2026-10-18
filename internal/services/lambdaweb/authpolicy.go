package lambdaweb

// authpolicy.go — the authorisation decision, free of HTTP so a table can drive
// it, and the small cache of compiled resource policies.

import (
	"container/list"
	"strings"
	"sync"

	"github.com/overcast-sh/overcast/internal/iampolicy"
)

// callerIdentity is who is calling: an IAM principal (ARN and Account; Known
// is false for an access key no IAM user or session owns) or a service
// (Service).
type callerIdentity struct {
	ARN     string
	Account string
	Known   bool
	Service string
}

// decisionInput is everything one authorisation decision reads. Context keys
// are lower-cased, as the evaluator looks them up.
type decisionInput struct {
	Action          string
	Resource        string
	EndpointAccount string
	Caller          callerIdentity
	Identity        []iampolicy.Statement
	Boundary        *iampolicy.Boundary
	ResourcePolicy  []iampolicy.Statement
	Context         map[string]string
}

// decision is the outcome, with the reason a refusal gives the debug log.
type decision struct {
	Allowed bool
	Reason  string
}

// decideInvoke applies AWS's cross-policy rules to a data-plane request:
//
//   - an explicit Deny in the identity policy, the permissions boundary or the
//     resource policy refuses the request;
//   - a service caller has no identity policy: only the resource policy allows;
//   - a caller in the endpoint's account is allowed by its identity policy OR
//     the resource policy;
//   - a caller in another account needs BOTH (an unknown principal has no
//     account and so counts as another account);
//   - a policy construct the evaluator does not implement refuses, as IAM
//     enforcement does: guessing could admit what AWS refuses.
func decideInvoke(in decisionInput) decision {
	req := iampolicy.Request{
		Action:           in.Action,
		Resource:         in.Resource,
		Context:          in.Context,
		PrincipalARN:     in.Caller.ARN,
		PrincipalAccount: in.Caller.Account,
		ServicePrincipal: in.Caller.Service,
	}
	res := iampolicy.Evaluate(iampolicy.Input{Request: req, ResourcePolicy: in.ResourcePolicy})
	if d, ok := refusal(res, "resource policy"); ok {
		return d
	}
	if in.Caller.Service != "" {
		if res.Decision != iampolicy.DecisionAllowed {
			return decision{Reason: "resource policy does not allow " + in.Caller.Service}
		}
		return decision{Allowed: true}
	}

	id := iampolicy.Evaluate(iampolicy.Input{Request: req, Identity: in.Identity, Boundary: in.Boundary})
	if d, ok := refusal(id, "identity policy"); ok {
		return d
	}
	byIdentity := id.Decision == iampolicy.DecisionAllowed
	byResource := res.Decision == iampolicy.DecisionAllowed
	sameAccount := in.Caller.Known && in.Caller.Account != "" && in.Caller.Account == in.EndpointAccount

	switch {
	case sameAccount && (byIdentity || byResource):
		return decision{Allowed: true}
	case !sameAccount && byIdentity && byResource:
		return decision{Allowed: true}
	case !sameAccount:
		return decision{Reason: "cross-account access needs both the identity policy and the resource policy to allow it"}
	}
	return decision{Reason: "neither the identity policy nor the resource policy allows " + in.Action}
}

// refusal turns an explicit deny, or an evaluation the evaluator cannot stand
// behind, into a refusal.
func refusal(r iampolicy.Result, source string) (decision, bool) {
	if r.Decision == iampolicy.DecisionExplicitDeny {
		return decision{Reason: "explicit deny in " + source}, true
	}
	if len(r.Unsupported) > 0 {
		return decision{Reason: source + " uses a construct the evaluator does not implement: " + strings.Join(r.Unsupported, "; ")}, true
	}
	return decision{}, false
}

// ─── Compiled resource policies ──────────────────────────────────────────────

type policyEntry struct {
	stmts []iampolicy.Statement
	err   error
}

// policyCache is a small LRU of compiled resource policies keyed by function
// ARN and policy revision id. A new revision id is a new key, so an updated
// policy is never served stale; the superseded entry ages out.
type policyCache struct {
	mu    sync.Mutex
	cap   int
	order *list.List // front = most recent; values are *policyItem
	items map[string]*list.Element
}

type policyItem struct {
	key   string
	entry policyEntry
}

func newPolicyCache(capacity int) *policyCache {
	return &policyCache{cap: capacity, order: list.New(), items: make(map[string]*list.Element, capacity)}
}

func (c *policyCache) get(key string) (policyEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return policyEntry{}, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*policyItem).entry, true
}

func (c *policyCache) put(key string, e policyEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value.(*policyItem).entry = e
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(&policyItem{key: key, entry: e})
	for c.order.Len() > c.cap {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.items, last.Value.(*policyItem).key)
	}
}
