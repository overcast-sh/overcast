package lambdaweb

// State access for Lambda Web Functions. Every record is one JSON blob in the
// generic store, keyed by region so the same function name in two regions is
// two resources, as it is on AWS:
//
//	lambda-web:functions  {region}/{function}
//	lambda-web:revisions  {region}/{function}/{revisionId}
//	lambda-web:endpoints  {region}/{function}/{endpoint}
//	lambda-web:policies   {function ARN}
//	lambda-web:tags       {function ARN}
//
// Reads isolate a record that will not decode (AGENTS.md § State): a listing
// skips and logs it, and a named read answers as the AWS not-found for that
// resource. Only a failing store is an InternalError.

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

const (
	nsFunctions = "lambda-web:functions"
	nsRevisions = "lambda-web:revisions"
	nsEndpoints = "lambda-web:endpoints"
	nsPolicies  = "lambda-web:policies"
	nsTags      = "lambda-web:tags"
)

func functionKey(region, name string) string { return serviceutil.RegionKey(region, name) }

func childKey(region, function, child string) string {
	return serviceutil.RegionKey(region, function+"/"+child)
}

// childPrefix is the key prefix every child of a function shares.
func childPrefix(region, function string) string {
	return serviceutil.RegionKey(region, function+"/")
}

func getRecord[T any](ctx context.Context, s *Service, ns, key string) (*T, bool, *protocol.AWSError) {
	raw, found, err := s.store.Get(ctx, ns, key)
	if err != nil {
		return nil, false, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, false, nil
	}
	var v T
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		s.logSkippedRecord(ns, key, err)
		return nil, false, nil
	}
	return &v, true, nil
}

// scanRecords decodes every record under prefix in key order, skipping and
// logging the ones that will not decode.
func scanRecords[T any](ctx context.Context, s *Service, ns, prefix string) ([]*T, *protocol.AWSError) {
	pairs, err := s.store.Scan(ctx, ns, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Key < pairs[j].Key })
	out := make([]*T, 0, len(pairs))
	for _, kv := range pairs {
		var v T
		if err := json.Unmarshal([]byte(kv.Value), &v); err != nil {
			s.logSkippedRecord(ns, kv.Key, err)
			continue
		}
		out = append(out, &v)
	}
	return out, nil
}

func putRecord(ctx context.Context, s *Service, ns, key string, v any) *protocol.AWSError {
	raw, err := json.Marshal(v)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.store.Set(ctx, ns, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func deleteRecord(ctx context.Context, s *Service, ns, key string) *protocol.AWSError {
	if err := s.store.Delete(ctx, ns, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *Service) loadFunction(ctx context.Context, region, name string) (*webFunction, bool, *protocol.AWSError) {
	return getRecord[webFunction](ctx, s, nsFunctions, functionKey(region, name))
}

func (s *Service) loadRevision(ctx context.Context, region, function, id string) (*revision, bool, *protocol.AWSError) {
	return getRecord[revision](ctx, s, nsRevisions, childKey(region, function, id))
}

func (s *Service) loadEndpoint(ctx context.Context, region, function, name string) (*endpoint, bool, *protocol.AWSError) {
	return getRecord[endpoint](ctx, s, nsEndpoints, childKey(region, function, name))
}

// listRevisions is a function's revisions, oldest first.
func (s *Service) listRevisions(ctx context.Context, region, function string) ([]*revision, *protocol.AWSError) {
	revs, aerr := scanRecords[revision](ctx, s, nsRevisions, childPrefix(region, function))
	if aerr != nil {
		return nil, aerr
	}
	sort.SliceStable(revs, func(i, j int) bool {
		if revs[i].Seq != revs[j].Seq {
			return revs[i].Seq < revs[j].Seq
		}
		return revs[i].CreatedAt.Before(revs[j].CreatedAt)
	})
	return revs, nil
}

// listEndpoints is a function's endpoints in name order.
func (s *Service) listEndpoints(ctx context.Context, region, function string) ([]*endpoint, *protocol.AWSError) {
	return scanRecords[endpoint](ctx, s, nsEndpoints, childPrefix(region, function))
}

func (s *Service) tagStore() *serviceutil.NSStore {
	return &serviceutil.NSStore{Store: s.store, NS: nsTags}
}
