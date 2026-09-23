package s3tables

// REST-JSON adapters. Each route is the model's @http binding, split by its
// root segment because the main router mounts each root separately behind the
// signing-name dispatcher. An adapter only lifts the binding's labels, query
// members and body into the typed request; typed_logic.go decides everything.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// Roots are the first path segments of every S3 Tables binding. Each is also
// a legal S3 bucket name.
var Roots = []string{
	"/buckets",
	"/namespaces",
	"/tables",
	"/get-table",
	"/tag",
	"/table-bucket-replication",
	"/table-replication",
	"/table-record-expiration",
	"/table-record-expiration-job-status",
	"/replication-status",
}

// RootRouters returns one sub-router per entry of Roots, with patterns
// relative to the root, for the main router to mount behind its dispatcher.
func (s *Service) RootRouters() map[string]chi.Router {
	routers := make(map[string]chi.Router, len(Roots))
	for _, root := range Roots {
		routers[root] = chi.NewRouter()
	}

	b := routers["/buckets"]
	b.Put("/", serve(http.StatusOK, bodyOnly[createTableBucketRequest], s.createTableBucketTyped))
	b.Get("/", serve(http.StatusOK, fillListTableBuckets, s.listTableBucketsTyped))
	b.Get("/{tableBucketARN}", serve(http.StatusOK, fillBucketARN, s.getTableBucketTyped))
	b.Delete("/{tableBucketARN}", serveAny(http.StatusNoContent, fillBucketARN, s.deleteTableBucketTyped))
	b.Put("/{tableBucketARN}/encryption", serveAny(http.StatusOK, withBucketBody(func(q *encryptionRequest, a string) { q.TableBucketARN = a }), s.putTableBucketEncryptionTyped))
	b.Get("/{tableBucketARN}/encryption", serve(http.StatusOK, fillBucketARN, s.getTableBucketEncryptionTyped))
	b.Delete("/{tableBucketARN}/encryption", serveAny(http.StatusNoContent, fillBucketARN, s.deleteTableBucketEncryptionTyped))
	b.Get("/{tableBucketARN}/maintenance", serve(http.StatusOK, fillBucketARN, s.getTableBucketMaintenanceConfigurationTyped))
	b.Put("/{tableBucketARN}/maintenance/{type}", serveAny(http.StatusNoContent, fillPutBucketMaintenance, s.putTableBucketMaintenanceConfigurationTyped))
	b.Put("/{tableBucketARN}/metrics", serveAny(http.StatusNoContent, fillBucketARN, s.putTableBucketMetricsConfigurationTyped))
	b.Get("/{tableBucketARN}/metrics", serve(http.StatusOK, fillBucketARN, s.getTableBucketMetricsConfigurationTyped))
	b.Delete("/{tableBucketARN}/metrics", serveAny(http.StatusNoContent, fillBucketARN, s.deleteTableBucketMetricsConfigurationTyped))
	b.Put("/{tableBucketARN}/policy", serveAny(http.StatusOK, withBucketBody(func(q *bucketPolicyRequest, a string) { q.TableBucketARN = a }), s.putTableBucketPolicyTyped))
	b.Get("/{tableBucketARN}/policy", serve(http.StatusOK, fillBucketARN, s.getTableBucketPolicyTyped))
	b.Delete("/{tableBucketARN}/policy", serveAny(http.StatusNoContent, fillBucketARN, s.deleteTableBucketPolicyTyped))
	b.Put("/{tableBucketARN}/storage-class", serveAny(http.StatusOK, withBucketBody(func(q *storageClassRequest, a string) { q.TableBucketARN = a }), s.putTableBucketStorageClassTyped))
	b.Get("/{tableBucketARN}/storage-class", serve(http.StatusOK, fillBucketARN, s.getTableBucketStorageClassTyped))

	n := routers["/namespaces"]
	n.Put("/{tableBucketARN}", serve(http.StatusOK, withBucketBody(func(q *createNamespaceRequest, a string) { q.TableBucketARN = a }), s.createNamespaceTyped))
	n.Get("/{tableBucketARN}", serve(http.StatusOK, fillListNamespaces, s.listNamespacesTyped))
	n.Get("/{tableBucketARN}/{namespace}", serve(http.StatusOK, fillNamespace, s.getNamespaceTyped))
	n.Delete("/{tableBucketARN}/{namespace}", serveAny(http.StatusNoContent, fillNamespace, s.deleteNamespaceTyped))

	t := routers["/tables"]
	t.Get("/{tableBucketARN}", serve(http.StatusOK, fillListTables, s.listTablesTyped))
	t.Put("/{tableBucketARN}/{namespace}", serve(http.StatusOK, fillCreateTable, s.createTableTyped))
	t.Delete("/{tableBucketARN}/{namespace}/{name}", serveAny(http.StatusNoContent, fillTable, s.deleteTableTyped))
	t.Get("/{tableBucketARN}/{namespace}/{name}/encryption", serve(http.StatusOK, fillTable, s.getTableEncryptionTyped))
	t.Get("/{tableBucketARN}/{namespace}/{name}/maintenance", serve(http.StatusOK, fillTable, s.getTableMaintenanceConfigurationTyped))
	t.Put("/{tableBucketARN}/{namespace}/{name}/maintenance/{type}", serveAny(http.StatusNoContent, fillPutTableMaintenance, s.putTableMaintenanceConfigurationTyped))
	t.Get("/{tableBucketARN}/{namespace}/{name}/maintenance-job-status", serve(http.StatusOK, fillTable, s.getTableMaintenanceJobStatusTyped))
	t.Get("/{tableBucketARN}/{namespace}/{name}/metadata-location", serve(http.StatusOK, fillTable, s.getTableMetadataLocationTyped))
	t.Put("/{tableBucketARN}/{namespace}/{name}/metadata-location", serve(http.StatusOK, fillUpdateMetadataLocation, s.updateTableMetadataLocationTyped))
	t.Put("/{tableBucketARN}/{namespace}/{name}/policy", serveAny(http.StatusOK, fillTablePolicy, s.putTablePolicyTyped))
	t.Get("/{tableBucketARN}/{namespace}/{name}/policy", serve(http.StatusOK, fillTable, s.getTablePolicyTyped))
	t.Delete("/{tableBucketARN}/{namespace}/{name}/policy", serveAny(http.StatusNoContent, fillTable, s.deleteTablePolicyTyped))
	t.Get("/{tableBucketARN}/{namespace}/{name}/storage-class", serve(http.StatusOK, fillTable, s.getTableStorageClassTyped))
	t.Put("/{tableBucketARN}/{namespace}/{name}/rename", serveAny(http.StatusNoContent, fillRenameTable, s.renameTableTyped))

	routers["/get-table"].Get("/", serve(http.StatusOK, fillGetTable, s.getTableTyped))

	tag := routers["/tag"]
	tag.Get("/{resourceArn}", serve(http.StatusOK, fillListTags, s.listTagsForResourceTyped))
	tag.Post("/{resourceArn}", serveAny(http.StatusOK, fillTagResource, s.tagResourceTyped))
	tag.Delete("/{resourceArn}", serveAny(http.StatusNoContent, fillUntagResource, s.untagResourceTyped))

	bktRepl := routers["/table-bucket-replication"]
	bktRepl.Put("/", serve(http.StatusOK, fillBucketReplication, s.putTableBucketReplicationTyped))
	bktRepl.Get("/", serve(http.StatusOK, fillBucketReplication, s.getTableBucketReplicationTyped))
	bktRepl.Delete("/", serveAny(http.StatusNoContent, fillBucketReplication, s.deleteTableBucketReplicationTyped))

	tblRepl := routers["/table-replication"]
	tblRepl.Put("/", serve(http.StatusOK, fillTableReplication, s.putTableReplicationTyped))
	tblRepl.Get("/", serve(http.StatusOK, fillTableARN, s.getTableReplicationTyped))
	tblRepl.Delete("/", serveAny(http.StatusNoContent, fillTableARN, s.deleteTableReplicationTyped))

	expiry := routers["/table-record-expiration"]
	expiry.Put("/", serveAny(http.StatusNoContent, fillPutRecordExpiration, s.putTableRecordExpirationConfigurationTyped))
	expiry.Get("/", serve(http.StatusOK, fillTableARN, s.getTableRecordExpirationConfigurationTyped))

	routers["/table-record-expiration-job-status"].Get("/", serve(http.StatusOK, fillTableARN, s.getTableRecordExpirationJobStatusTyped))
	routers["/replication-status"].Get("/", serve(http.StatusOK, fillTableARN, s.getTableReplicationStatusTyped))

	return routers
}

// ─── Serving ──────────────────────────────────────────────────────────────────

type filler[In any] func(*http.Request, *In) *protocol.AWSError

// serve runs an operation with a modeled output shape.
func serve[In, Out any](status int, fill filler[In], fn func(context.Context, *In) (*Out, *protocol.AWSError)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req In
		if aerr := fill(r, &req); aerr != nil {
			protocol.WriteJSONError(w, r, aerr)
			return
		}
		out, aerr := fn(r.Context(), &req)
		if aerr != nil {
			protocol.WriteJSONError(w, r, aerr)
			return
		}
		protocol.WriteJSON(w, r, status, out)
	}
}

// serveAny runs an operation whose output shape is empty: 204 with no body,
// or 200 with an empty object, as the operation's binding says.
func serveAny[In any](status int, fill filler[In], fn func(context.Context, *In) (any, *protocol.AWSError)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req In
		if aerr := fill(r, &req); aerr != nil {
			protocol.WriteJSONError(w, r, aerr)
			return
		}
		if _, aerr := fn(r.Context(), &req); aerr != nil {
			protocol.WriteJSONError(w, r, aerr)
			return
		}
		if status == http.StatusNoContent {
			protocol.WriteEmpty(w, r, status)
			return
		}
		protocol.WriteJSON(w, r, status, struct{}{})
	}
}

// ─── Binding helpers ──────────────────────────────────────────────────────────

// decodeBody reads a JSON body into dst. An absent body leaves dst zero, so
// the operation's own required-member checks answer for it.
func decodeBody(r *http.Request, dst any) *protocol.AWSError {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	err := json.NewDecoder(r.Body).Decode(dst)
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	return badRequest("The request body could not be parsed as JSON.")
}

// label reads a path label. SDKs percent-encode a label that holds an ARN into
// one segment, and chi matches on the escaped path, so it is unescaped here.
func label(r *http.Request, name string) string {
	raw := chi.URLParam(r, name)
	if v, err := url.PathUnescape(raw); err == nil {
		return v
	}
	return raw
}

func queryInt(r *http.Request, name string) (*int, *protocol.AWSError) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return nil, badRequest("The value of " + name + " must be an integer.")
	}
	return &v, nil
}

// fillBucketARN binds the {tableBucketARN} label of the operations that take
// nothing else.
func fillBucketARN(r *http.Request, req *tableBucketARNRequest) *protocol.AWSError {
	req.TableBucketARN = label(r, "tableBucketARN")
	return nil
}

// withBucketBody decodes an operation's body and then binds its
// {tableBucketARN} label through set, so the label always wins over a body
// member of the same name.
func withBucketBody[In any](set func(*In, string)) filler[In] {
	return func(r *http.Request, req *In) *protocol.AWSError {
		if aerr := decodeBody(r, req); aerr != nil {
			return aerr
		}
		set(req, label(r, "tableBucketARN"))
		return nil
	}
}

func bodyOnly[In any](r *http.Request, req *In) *protocol.AWSError {
	return decodeBody(r, req)
}

func fillListTableBuckets(r *http.Request, req *listTableBucketsRequest) *protocol.AWSError {
	q := r.URL.Query()
	req.Prefix, req.ContinuationToken, req.Type = q.Get("prefix"), q.Get("continuationToken"), q.Get("type")
	var aerr *protocol.AWSError
	req.MaxBuckets, aerr = queryInt(r, "maxBuckets")
	return aerr
}

func fillPutBucketMaintenance(r *http.Request, req *putBucketMaintenanceRequest) *protocol.AWSError {
	if aerr := decodeBody(r, req); aerr != nil {
		return aerr
	}
	req.TableBucketARN, req.Type = label(r, "tableBucketARN"), label(r, "type")
	return nil
}

func fillListNamespaces(r *http.Request, req *listNamespacesRequest) *protocol.AWSError {
	q := r.URL.Query()
	req.TableBucketARN = label(r, "tableBucketARN")
	req.Prefix, req.ContinuationToken = q.Get("prefix"), q.Get("continuationToken")
	var aerr *protocol.AWSError
	req.MaxNamespaces, aerr = queryInt(r, "maxNamespaces")
	return aerr
}

func fillNamespace(r *http.Request, req *namespaceRequest) *protocol.AWSError {
	req.TableBucketARN, req.Namespace = label(r, "tableBucketARN"), label(r, "namespace")
	return nil
}

func fillListTables(r *http.Request, req *listTablesRequest) *protocol.AWSError {
	q := r.URL.Query()
	req.TableBucketARN = label(r, "tableBucketARN")
	req.Namespace, req.Prefix, req.ContinuationToken = q.Get("namespace"), q.Get("prefix"), q.Get("continuationToken")
	var aerr *protocol.AWSError
	req.MaxTables, aerr = queryInt(r, "maxTables")
	return aerr
}

func fillCreateTable(r *http.Request, req *createTableRequest) *protocol.AWSError {
	if aerr := decodeBody(r, req); aerr != nil {
		return aerr
	}
	req.TableBucketARN, req.Namespace = label(r, "tableBucketARN"), label(r, "namespace")
	return nil
}

// fillTable binds the {tableBucketARN}/{namespace}/{name} labels, plus the
// versionToken query member DeleteTable carries.
func fillTable(r *http.Request, req *tableRequest) *protocol.AWSError {
	req.TableBucketARN, req.Namespace, req.Name = label(r, "tableBucketARN"), label(r, "namespace"), label(r, "name")
	req.VersionToken = r.URL.Query().Get("versionToken")
	return nil
}

func fillPutTableMaintenance(r *http.Request, req *putTableMaintenanceRequest) *protocol.AWSError {
	if aerr := decodeBody(r, req); aerr != nil {
		return aerr
	}
	req.TableBucketARN, req.Namespace, req.Name = label(r, "tableBucketARN"), label(r, "namespace"), label(r, "name")
	req.Type = label(r, "type")
	return nil
}

func fillUpdateMetadataLocation(r *http.Request, req *updateMetadataLocationRequest) *protocol.AWSError {
	if aerr := decodeBody(r, req); aerr != nil {
		return aerr
	}
	req.TableBucketARN, req.Namespace, req.Name = label(r, "tableBucketARN"), label(r, "namespace"), label(r, "name")
	return nil
}

func fillTablePolicy(r *http.Request, req *tablePolicyRequest) *protocol.AWSError {
	if aerr := decodeBody(r, req); aerr != nil {
		return aerr
	}
	req.TableBucketARN, req.Namespace, req.Name = label(r, "tableBucketARN"), label(r, "namespace"), label(r, "name")
	return nil
}

func fillRenameTable(r *http.Request, req *renameTableRequest) *protocol.AWSError {
	if aerr := decodeBody(r, req); aerr != nil {
		return aerr
	}
	req.TableBucketARN, req.Namespace, req.Name = label(r, "tableBucketARN"), label(r, "namespace"), label(r, "name")
	return nil
}

func fillGetTable(r *http.Request, req *getTableRequest) *protocol.AWSError {
	q := r.URL.Query()
	req.TableBucketARN, req.Namespace, req.Name, req.TableARN = q.Get("tableBucketARN"), q.Get("namespace"), q.Get("name"), q.Get("tableArn")
	return nil
}

func fillListTags(r *http.Request, req *listTagsRequest) *protocol.AWSError {
	req.ResourceARN = label(r, "resourceArn")
	return nil
}

func fillTagResource(r *http.Request, req *tagResourceRequest) *protocol.AWSError {
	if aerr := decodeBody(r, req); aerr != nil {
		return aerr
	}
	req.ResourceARN = label(r, "resourceArn")
	return nil
}

func fillUntagResource(r *http.Request, req *untagResourceRequest) *protocol.AWSError {
	req.ResourceARN = label(r, "resourceArn")
	req.TagKeys = r.URL.Query()["tagKeys"]
	return nil
}

func fillBucketReplication(r *http.Request, req *bucketReplicationRequest) *protocol.AWSError {
	if r.Method == http.MethodPut {
		if aerr := decodeBody(r, req); aerr != nil {
			return aerr
		}
	}
	q := r.URL.Query()
	req.TableBucketARN, req.VersionToken = q.Get("tableBucketARN"), q.Get("versionToken")
	return nil
}

func fillTableReplication(r *http.Request, req *tableReplicationRequest) *protocol.AWSError {
	if aerr := decodeBody(r, req); aerr != nil {
		return aerr
	}
	q := r.URL.Query()
	req.TableARN, req.VersionToken = q.Get("tableArn"), q.Get("versionToken")
	return nil
}

func fillTableARN(r *http.Request, req *tableARNRequest) *protocol.AWSError {
	q := r.URL.Query()
	req.TableARN, req.VersionToken = q.Get("tableArn"), q.Get("versionToken")
	return nil
}

func fillPutRecordExpiration(r *http.Request, req *putRecordExpirationRequest) *protocol.AWSError {
	if aerr := decodeBody(r, req); aerr != nil {
		return aerr
	}
	req.TableARN = r.URL.Query().Get("tableArn")
	return nil
}
