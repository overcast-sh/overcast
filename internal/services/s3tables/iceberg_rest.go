package s3tables

// The Iceberg REST catalog AWS serves for S3 Tables at
// https://s3tables.<region>.amazonaws.com/iceberg, which lets PyIceberg,
// Spark, Trino and DuckDB use table buckets as a catalog. The {prefix} of
// every path is the URL-encoded table bucket ARN, which /v1/config hands the
// client as an override of its warehouse.
//
// It is not part of the S3 Tables API model, so it follows the Iceberg REST
// spec rather than the service's REST-JSON conventions: its own request and
// response shapes, and the spec's error model
// ({"error":{"message","type","code"}}) instead of AWS's.
//
// Spec: https://github.com/apache/iceberg/blob/main/open-api/rest-catalog-open-api.yaml

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// IcebergRoot is where AWS serves the catalog: every path under it is the
// catalog's, but "iceberg" is also a legal S3 bucket name, so the main router
// sends a request here only when it is signed for "s3tables".
const IcebergRoot = "/iceberg"

// IcebergInternalRoot mounts the same catalog for unsigned clients.
const IcebergInternalRoot = middleware.InternalPrefix + "s3tables/iceberg"

// IcebergRouter returns the catalog's routes, relative to IcebergRoot or
// IcebergInternalRoot.
func (s *Service) IcebergRouter() chi.Router {
	r := chi.NewRouter()
	r.NotFound(icebergUnsupported)
	r.MethodNotAllowed(icebergUnsupported)
	r.Get("/v1/config", s.IcebergGetConfig)
	for endpoint, h := range s.icebergEndpoints() {
		method, pattern, _ := strings.Cut(endpoint, " ")
		r.Method(method, pattern, h)
	}
	return r
}

// icebergEndpoints are the catalog's endpoints under a table bucket's prefix,
// keyed as the spec names an endpoint — "<METHOD> <path template>" — which is
// also the list /v1/config advertises, so the two cannot drift apart.
func (s *Service) icebergEndpoints() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /v1/{prefix}/namespaces":                                     s.IcebergListNamespaces,
		"POST /v1/{prefix}/namespaces":                                    s.IcebergCreateNamespace,
		"GET /v1/{prefix}/namespaces/{namespace}":                         s.IcebergLoadNamespaceMetadata,
		"HEAD /v1/{prefix}/namespaces/{namespace}":                        s.IcebergNamespaceExists,
		"DELETE /v1/{prefix}/namespaces/{namespace}":                      s.IcebergDropNamespace,
		"POST /v1/{prefix}/namespaces/{namespace}/properties":             s.IcebergUpdateProperties,
		"GET /v1/{prefix}/namespaces/{namespace}/tables":                  s.IcebergListTables,
		"POST /v1/{prefix}/namespaces/{namespace}/tables":                 s.IcebergCreateTable,
		"POST /v1/{prefix}/namespaces/{namespace}/register":               s.IcebergRegisterTable,
		"GET /v1/{prefix}/namespaces/{namespace}/tables/{table}":          s.IcebergLoadTable,
		"HEAD /v1/{prefix}/namespaces/{namespace}/tables/{table}":         s.IcebergTableExists,
		"POST /v1/{prefix}/namespaces/{namespace}/tables/{table}":         s.IcebergUpdateTable,
		"DELETE /v1/{prefix}/namespaces/{namespace}/tables/{table}":       s.IcebergDropTable,
		"POST /v1/{prefix}/namespaces/{namespace}/tables/{table}/metrics": s.IcebergReportMetrics,
		"POST /v1/{prefix}/tables/rename":                                 s.IcebergRenameTable,
	}
}

// ─── Handlers ─────────────────────────────────────────────────────────────────
//
// One per endpoint, named "Iceberg" plus the spec's operationId, which is the
// name capabilities_dev.go declares it under.

// IcebergGetConfig serves getConfig.
func (s *Service) IcebergGetConfig(w http.ResponseWriter, r *http.Request) {
	serveIceberg(http.StatusOK, fillIcebergConfig, s.icebergConfigTyped)(w, r)
}

// IcebergListNamespaces serves listNamespaces.
func (s *Service) IcebergListNamespaces(w http.ResponseWriter, r *http.Request) {
	serveIceberg(http.StatusOK, fillIcebergList, s.icebergListNamespacesTyped)(w, r)
}

// IcebergCreateNamespace serves createNamespace.
func (s *Service) IcebergCreateNamespace(w http.ResponseWriter, r *http.Request) {
	serveIceberg(http.StatusOK, withIcebergBody[icebergCreateNamespaceRequest], s.icebergCreateNamespaceTyped)(w, r)
}

// IcebergLoadNamespaceMetadata serves loadNamespaceMetadata.
func (s *Service) IcebergLoadNamespaceMetadata(w http.ResponseWriter, r *http.Request) {
	serveIceberg(http.StatusOK, fillIcebergPath, s.icebergLoadNamespaceTyped)(w, r)
}

// IcebergNamespaceExists serves namespaceExists.
func (s *Service) IcebergNamespaceExists(w http.ResponseWriter, r *http.Request) {
	serveIcebergEmpty(fillIcebergPath, s.icebergNamespaceExistsTyped)(w, r)
}

// IcebergDropNamespace serves dropNamespace.
func (s *Service) IcebergDropNamespace(w http.ResponseWriter, r *http.Request) {
	serveIcebergEmpty(fillIcebergPath, s.icebergDropNamespaceTyped)(w, r)
}

// IcebergUpdateProperties serves updateProperties.
func (s *Service) IcebergUpdateProperties(w http.ResponseWriter, r *http.Request) {
	serveIceberg(http.StatusOK, withIcebergBody[icebergNamespacePropertiesRequest], s.icebergUpdateNamespacePropertiesTyped)(w, r)
}

// IcebergListTables serves listTables.
func (s *Service) IcebergListTables(w http.ResponseWriter, r *http.Request) {
	serveIceberg(http.StatusOK, fillIcebergList, s.icebergListTablesTyped)(w, r)
}

// IcebergCreateTable serves createTable, staged or not.
func (s *Service) IcebergCreateTable(w http.ResponseWriter, r *http.Request) {
	serveIceberg(http.StatusOK, withIcebergBody[icebergCreateTableRequest], s.icebergCreateTableTyped)(w, r)
}

// IcebergRegisterTable serves registerTable.
func (s *Service) IcebergRegisterTable(w http.ResponseWriter, r *http.Request) {
	serveIceberg(http.StatusOK, withIcebergBody[icebergRegisterTableRequest], s.icebergRegisterTableTyped)(w, r)
}

// IcebergLoadTable serves loadTable.
func (s *Service) IcebergLoadTable(w http.ResponseWriter, r *http.Request) {
	serveIceberg(http.StatusOK, fillIcebergPath, s.icebergLoadTableTyped)(w, r)
}

// IcebergTableExists serves tableExists.
func (s *Service) IcebergTableExists(w http.ResponseWriter, r *http.Request) {
	serveIcebergEmpty(fillIcebergPath, s.icebergTableExistsTyped)(w, r)
}

// IcebergUpdateTable serves updateTable, the commit.
func (s *Service) IcebergUpdateTable(w http.ResponseWriter, r *http.Request) {
	serveIceberg(http.StatusOK, withIcebergBody[icebergCommitTableRequest], s.icebergCommitTableTyped)(w, r)
}

// IcebergDropTable serves dropTable.
func (s *Service) IcebergDropTable(w http.ResponseWriter, r *http.Request) {
	serveIcebergEmpty(fillIcebergDropTable, s.icebergDropTableTyped)(w, r)
}

// IcebergRenameTable serves renameTable.
func (s *Service) IcebergRenameTable(w http.ResponseWriter, r *http.Request) {
	serveIcebergEmpty(withIcebergBody[icebergRenameTableRequest], s.icebergRenameTableTyped)(w, r)
}

// IcebergReportMetrics serves reportMetrics by accepting the report and
// dropping it: Overcast keeps no scan or commit metrics, and a client that is
// refused logs a warning after every scan.
func (s *Service) IcebergReportMetrics(w http.ResponseWriter, r *http.Request) {
	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// ─── Errors ───────────────────────────────────────────────────────────────────

// icebergErrorResponse is the spec's IcebergErrorResponse.
type icebergErrorResponse struct {
	Error icebergErrorModel `json:"error"`
}

type icebergErrorModel struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    int    `json:"code"`
}

// icebergError is an error the catalog answers with an Iceberg exception type.
// It travels as a protocol.AWSError so the catalog shares the service's
// resolution and validation code; writeIcebergError puts it on the wire.
func icebergError(status int, typ, msg string) *protocol.AWSError {
	return &protocol.AWSError{Code: typ, Message: msg, HTTPStatus: status}
}

// icebergErrorTypes names the Iceberg exception an S3 Tables error is answered
// as, where the spec has one; the clients choose their exception by it.
var icebergErrorTypes = map[*protocol.AWSError]string{
	errNamespaceNotFound: "NoSuchNamespaceException",
	errDestNamespace:     "NoSuchNamespaceException",
	errTableNotFound:     "NoSuchTableException",
	errNamespaceExists:   "AlreadyExistsException",
	errTableExists:       "AlreadyExistsException",
	errNamespaceNotEmpty: "NamespaceNotEmptyException",
}

// writeIcebergError writes aerr in the spec's error model. Anything without an
// Iceberg exception keeps its S3 Tables code (BadRequestException is the same
// name in both), except server faults, which never reveal their cause.
func writeIcebergError(w http.ResponseWriter, r *http.Request, aerr *protocol.AWSError) {
	protocol.RecordError(w, aerr)
	typ, ok := icebergErrorTypes[aerr]
	switch {
	case ok:
	case aerr.HTTPStatus == http.StatusNotImplemented:
		typ = "UnsupportedOperationException"
	case aerr.HTTPStatus >= http.StatusInternalServerError:
		typ, aerr = "InternalServerError", protocol.ErrInternalError
	default:
		typ = aerr.Code
	}
	writeIcebergJSON(w, r, aerr.HTTPStatus, icebergErrorResponse{Error: icebergErrorModel{Message: aerr.Message, Type: typ, Code: aerr.HTTPStatus}})
}

func writeIcebergJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	protocol.WriteAWSJSON(w, r, status, v, "application/json")
}

// icebergUnsupported answers an endpoint the catalog does not serve — views,
// multi-table transactions, scan planning — as the spec's clients expect an
// unimplemented endpoint to answer.
func icebergUnsupported(w http.ResponseWriter, r *http.Request) {
	writeIcebergError(w, r, icebergError(http.StatusNotImplemented, "UnsupportedOperationException",
		"The Iceberg REST endpoint "+r.Method+" "+r.URL.Path+" is not implemented by Overcast's S3 Tables catalog."))
}

// ─── Serving ──────────────────────────────────────────────────────────────────

// serveIceberg runs a catalog operation that answers with a body.
func serveIceberg[In, Out any](status int, fill filler[In], fn func(context.Context, *In) (*Out, *protocol.AWSError)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req In
		if aerr := fill(r, &req); aerr != nil {
			writeIcebergError(w, r, aerr)
			return
		}
		out, aerr := fn(r.Context(), &req)
		if aerr != nil {
			writeIcebergError(w, r, aerr)
			return
		}
		writeIcebergJSON(w, r, status, out)
	}
}

// serveIcebergEmpty runs a catalog operation that answers 204 No Content.
func serveIcebergEmpty[In any](fill filler[In], fn func(context.Context, *In) *protocol.AWSError) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req In
		if aerr := fill(r, &req); aerr != nil {
			writeIcebergError(w, r, aerr)
			return
		}
		if aerr := fn(r.Context(), &req); aerr != nil {
			writeIcebergError(w, r, aerr)
			return
		}
		protocol.WriteEmpty(w, r, http.StatusNoContent)
	}
}

// icebergPath is what a catalog path addresses: the table bucket its {prefix}
// names, and the namespace and table when the path has them. A namespace in
// a path joins its levels with the unit separator; S3 Tables namespaces have
// one level.
type icebergPath struct {
	BucketARN string `json:"-"`
	Namespace string `json:"-"`
	Table     string `json:"-"`
}

func (p *icebergPath) bind(r *http.Request) {
	p.BucketARN, p.Namespace, p.Table = label(r, "prefix"), label(r, "namespace"), label(r, "table")
}

// path makes every request that embeds icebergPath an icebergBody.
func (p *icebergPath) path() *icebergPath { return p }

func fillIcebergPath(r *http.Request, req *icebergPath) *protocol.AWSError {
	req.bind(r)
	return nil
}

// icebergBody is a request with a JSON body addressed by a catalog path.
type icebergBody interface {
	path() *icebergPath
}

// withIcebergBody decodes a request body and binds the path it was sent to.
func withIcebergBody[In any, P interface {
	*In
	icebergBody
}](r *http.Request, req *In) *protocol.AWSError {
	if aerr := decodeBody(r, req); aerr != nil {
		return aerr
	}
	P(req).path().bind(r)
	return nil
}

// icebergListRequest is a paged list of a bucket's namespaces or a
// namespace's tables.
type icebergListRequest struct {
	icebergPath
	Parent    string
	PageToken string
	PageSize  *int
}

func fillIcebergList(r *http.Request, req *icebergListRequest) *protocol.AWSError {
	req.bind(r)
	q := r.URL.Query()
	req.Parent, req.PageToken = q.Get("parent"), q.Get("pageToken")
	var aerr *protocol.AWSError
	req.PageSize, aerr = queryInt(r, "pageSize")
	return aerr
}

// singleLevel is the one level of an S3 Tables namespace given as a list.
func singleLevel(namespace []string) (string, *protocol.AWSError) {
	if len(namespace) != 1 {
		return "", badRequest("S3 Tables namespaces have exactly one level.")
	}
	return namespace[0], nil
}

// ─── Config ───────────────────────────────────────────────────────────────────

type icebergConfigRequest struct {
	Warehouse string
}

func fillIcebergConfig(r *http.Request, req *icebergConfigRequest) *protocol.AWSError {
	req.Warehouse = r.URL.Query().Get("warehouse")
	return nil
}

// icebergConfigResponse is the spec's CatalogConfig.
type icebergConfigResponse struct {
	Defaults  map[string]string `json:"defaults"`
	Overrides map[string]string `json:"overrides"`
	Endpoints []string          `json:"endpoints"`
}

// icebergConfigTyped answers GET /v1/config for the table bucket named by the
// warehouse parameter. The prefix override is what AWS returns. The defaults
// are Overcast's own: they point a client's FileIO at Overcast's S3 with
// path-style addressing, so data files land beside the metadata without any
// client configuration — and, being defaults, anything the client sets wins.
func (s *Service) icebergConfigTyped(ctx context.Context, req *icebergConfigRequest) (*icebergConfigResponse, *protocol.AWSError) {
	if req.Warehouse == "" {
		return nil, badRequest("The warehouse parameter must name a table bucket ARN.")
	}
	b, aerr := s.resolveBucket(ctx, req.Warehouse)
	if aerr != nil {
		return nil, aerr
	}
	endpoint := serviceutil.ClientBaseURLFromOrigin(s.cfg, middleware.ClientEndpointFromContext(ctx))
	return &icebergConfigResponse{
		Defaults: map[string]string{
			"s3.endpoint":          endpoint,
			"s3.path-style-access": "true",
			"s3.region":            b.Region,
			"client.region":        b.Region,
		},
		Overrides: map[string]string{"prefix": url.QueryEscape(b.ARN)},
		Endpoints: slices.Sorted(maps.Keys(s.icebergEndpoints())),
	}, nil
}
