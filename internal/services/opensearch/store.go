package opensearch

import (
	"context"
	"encoding/json"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const (
	nsDomains = "opensearch:domains"
	nsTags    = "opensearch:tags"
)

// osStore is every read and write OpenSearch makes against state.Store.
//
// Domain keys are region-scoped. AWS states the constraint on CreateDomain's
// DomainName: names are "unique across the domains owned by an account within
// an AWS Region", so two regions may legitimately hold the same name and a
// request must only ever see its own region's.
//
// Tags are keyed by the resource ARN instead, because that is what the tag
// operations are addressed by, and an ARN already carries its region.
type osStore struct {
	store state.Store
	log   *serviceutil.ServiceLogger
	tags  *serviceutil.NSStore
}

func newOSStore(st state.Store, log *serviceutil.ServiceLogger) *osStore {
	return &osStore{
		store: st,
		log:   log,
		tags:  &serviceutil.NSStore{Store: st, NS: nsTags},
	}
}

func (s *osStore) putDomain(ctx context.Context, region string, d *DomainStatus) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return s.store.Set(ctx, nsDomains, serviceutil.RegionKey(region, d.DomainName), string(raw))
}

// getDomain returns the domain called name in region.
//
// A record that cannot be decoded reads as absent rather than as a failure:
// one bad payload must not turn a describe into a 500, and
// ResourceNotFoundException is the answer AWS models for a name the service
// cannot produce (AGENTS.md § malformed persisted state must be isolated).
func (s *osStore) getDomain(ctx context.Context, region, name string) (*DomainStatus, bool, error) {
	raw, found, err := s.store.Get(ctx, nsDomains, serviceutil.RegionKey(region, name))
	if err != nil || !found {
		return nil, false, err
	}
	var d DomainStatus
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		s.log.Warn("skipping undecodable domain record", zap.String("domain", name), zap.Error(err))
		return nil, false, nil
	}
	// Both read paths fill DomainStatus's required members here rather than in
	// each of the four handlers, so a record written before ClusterConfig was
	// one of them still answers with the whole modeled shape.
	d.ensureClusterConfig()
	return &d, true, nil
}

// listDomains returns every domain in region, skipping records that cannot be
// decoded so that one bad payload does not fail the whole listing.
func (s *osStore) listDomains(ctx context.Context, region string) ([]DomainStatus, error) {
	pairs, err := s.store.Scan(ctx, nsDomains, serviceutil.RegionKey(region, ""))
	if err != nil {
		return nil, err
	}
	out := make([]DomainStatus, 0, len(pairs))
	for _, kv := range pairs {
		var d DomainStatus
		if err := json.Unmarshal([]byte(kv.Value), &d); err != nil {
			s.log.Warn("skipping undecodable domain record", zap.String("key", kv.Key), zap.Error(err))
			continue
		}
		d.ensureClusterConfig()
		out = append(out, d)
	}
	return out, nil
}

// deleteDomain removes the domain and the tags attached to it. The tag blob is
// keyed by ARN because that is what AddTags writes it under; deleting it by
// domain name, as this did before, left every deleted domain's tags behind for
// the next domain of the same name to inherit.
func (s *osStore) deleteDomain(ctx context.Context, region string, d *DomainStatus) error {
	if err := s.store.Delete(ctx, nsTags, d.ARN); err != nil {
		return err
	}
	return s.store.Delete(ctx, nsDomains, serviceutil.RegionKey(region, d.DomainName))
}
