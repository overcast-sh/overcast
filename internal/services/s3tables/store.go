package s3tables

// State access for S3 Tables. Every record is one JSON blob in the generic
// store, keyed by region so the same bucket name in two regions is two
// resources, as it is on AWS (table bucket names are unique per account and
// Region, not globally):
//
//	s3tables:buckets     {region}/{bucket}
//	s3tables:namespaces  {region}/{bucket}/{namespace}
//	s3tables:tables      {region}/{bucket}/{namespace}/{table}
//
// Reads isolate a record that will not decode (AGENTS.md § State): a listing
// skips and logs it, and a named read answers as the AWS not-found for that
// resource. Only a failing store is an InternalError.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

const (
	nsBuckets    = "s3tables:buckets"
	nsNamespaces = "s3tables:namespaces"
	nsTables     = "s3tables:tables"
)

func bucketKey(region, bucket string) string {
	return serviceutil.RegionKey(region, bucket)
}

func namespaceKey(region, bucket, namespace string) string {
	return serviceutil.RegionKey(region, bucket+"/"+namespace)
}

func tableKey(region, bucket, namespace, name string) string {
	return serviceutil.RegionKey(region, bucket+"/"+namespace+"/"+name)
}

// getRecord reads and decodes one record. found is false for a missing key and
// for a record that will not decode; the latter is logged.
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

// scanRecords decodes every record under prefix, skipping (and logging) the
// ones that will not decode, in key order.
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

// hasAny reports whether any key exists under prefix, whether or not it
// decodes: a corrupt child still makes its parent non-empty.
func hasAny(ctx context.Context, s *Service, ns, prefix string) (bool, *protocol.AWSError) {
	pairs, err := s.store.Scan(ctx, ns, prefix)
	if err != nil {
		return false, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return len(pairs) > 0, nil
}

// ─── Buckets ──────────────────────────────────────────────────────────────────

func (s *Service) loadBucket(ctx context.Context, region, name string) (*tableBucket, bool, *protocol.AWSError) {
	return getRecord[tableBucket](ctx, s, nsBuckets, bucketKey(region, name))
}

func (s *Service) saveBucket(ctx context.Context, b *tableBucket) *protocol.AWSError {
	return putRecord(ctx, s, nsBuckets, bucketKey(b.Region, b.Name), b)
}

func (s *Service) listBuckets(ctx context.Context, region string) ([]*tableBucket, *protocol.AWSError) {
	return scanRecords[tableBucket](ctx, s, nsBuckets, region+"/")
}

// ─── Namespaces ───────────────────────────────────────────────────────────────

func (s *Service) loadNamespace(ctx context.Context, region, bucket, name string) (*namespaceRecord, bool, *protocol.AWSError) {
	return getRecord[namespaceRecord](ctx, s, nsNamespaces, namespaceKey(region, bucket, name))
}

func (s *Service) saveNamespace(ctx context.Context, region string, n *namespaceRecord) *protocol.AWSError {
	return putRecord(ctx, s, nsNamespaces, namespaceKey(region, n.Bucket, n.Name), n)
}

func (s *Service) listNamespaces(ctx context.Context, region, bucket string) ([]*namespaceRecord, *protocol.AWSError) {
	return scanRecords[namespaceRecord](ctx, s, nsNamespaces, namespaceKey(region, bucket, ""))
}

// ─── Tables ───────────────────────────────────────────────────────────────────

func (s *Service) loadTable(ctx context.Context, region, bucket, namespace, name string) (*tableRecord, bool, *protocol.AWSError) {
	return getRecord[tableRecord](ctx, s, nsTables, tableKey(region, bucket, namespace, name))
}

func (s *Service) saveTable(ctx context.Context, t *tableRecord) *protocol.AWSError {
	return putRecord(ctx, s, nsTables, tableKey(t.Region, t.Bucket, t.Namespace, t.Name), t)
}

func (s *Service) deleteTableRecord(ctx context.Context, t *tableRecord) *protocol.AWSError {
	return deleteRecord(ctx, s, nsTables, tableKey(t.Region, t.Bucket, t.Namespace, t.Name))
}

// listTables returns the tables of a bucket, or of one namespace in it when
// namespace is non-empty.
func (s *Service) listTables(ctx context.Context, region, bucket, namespace string) ([]*tableRecord, *protocol.AWSError) {
	prefix := bucketKey(region, bucket) + "/"
	if namespace != "" {
		prefix = namespaceKey(region, bucket, namespace) + "/"
	}
	return scanRecords[tableRecord](ctx, s, nsTables, prefix)
}

// findTableByID finds a table of a bucket by the id its ARN carries. A table's
// ARN survives RenameTable, so it cannot be a key; a bucket holds few enough
// tables for the scan to be the simpler answer than a second index to keep in
// step.
func (s *Service) findTableByID(ctx context.Context, region, bucket, id string) (*tableRecord, bool, *protocol.AWSError) {
	tables, aerr := s.listTables(ctx, region, bucket, "")
	if aerr != nil {
		return nil, false, aerr
	}
	for _, t := range tables {
		if strings.EqualFold(t.TableID, id) {
			return t, true, nil
		}
	}
	return nil, false, nil
}
