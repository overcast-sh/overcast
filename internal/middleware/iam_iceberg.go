package middleware

import (
	"net/http"
	"strings"
)

// icebergRoot is where S3 Tables serves its Iceberg REST catalog, on its own
// endpoint (s3tables.IcebergRoot).
const icebergRoot = "/iceberg"

// icebergEndpointIAMActions are the actions AWS checks for each Iceberg REST
// catalog endpoint, keyed "<METHOD> <path template>" as the Iceberg spec, and
// the s3tables package, name the endpoint. They are the table in "Accessing
// tables using the Amazon S3 Tables Iceberg REST endpoint"
// (https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-tables-integrating-open-source.html),
// which lists every action an endpoint checks: loading a table reads its
// metadata location and its data.
//
// Overcast also serves three endpoints AWS does not: updateProperties,
// registerTable and reportMetrics. AWS names no action for them, so they are
// authorised as every S3 Tables action (unnamedIAMAction).
var icebergEndpointIAMActions = map[string][]string{
	"GET /v1/config":                                            {"s3tables:GetTableBucket"},
	"GET /v1/{prefix}/namespaces":                               {"s3tables:ListNamespaces"},
	"POST /v1/{prefix}/namespaces":                              {"s3tables:CreateNamespace"},
	"GET /v1/{prefix}/namespaces/{namespace}":                   {"s3tables:GetNamespace"},
	"HEAD /v1/{prefix}/namespaces/{namespace}":                  {"s3tables:GetNamespace"},
	"DELETE /v1/{prefix}/namespaces/{namespace}":                {"s3tables:DeleteNamespace"},
	"GET /v1/{prefix}/namespaces/{namespace}/tables":            {"s3tables:ListTables"},
	"POST /v1/{prefix}/namespaces/{namespace}/tables":           {"s3tables:CreateTable", "s3tables:PutTableData"},
	"GET /v1/{prefix}/namespaces/{namespace}/tables/{table}":    {"s3tables:GetTableMetadataLocation", "s3tables:GetTableData"},
	"HEAD /v1/{prefix}/namespaces/{namespace}/tables/{table}":   {"s3tables:GetTable"},
	"POST /v1/{prefix}/namespaces/{namespace}/tables/{table}":   {"s3tables:UpdateTableMetadataLocation", "s3tables:PutTableData", "s3tables:GetTableData"},
	"DELETE /v1/{prefix}/namespaces/{namespace}/tables/{table}": {"s3tables:DeleteTable"},
	"POST /v1/{prefix}/tables/rename":                           {"s3tables:RenameTable"},
}

// icebergIAMActions are the actions AWS checks for the Iceberg REST catalog
// call r is, or nil when r is none it names.
func icebergIAMActions(r *http.Request) []string {
	return icebergEndpointIAMActions[icebergEndpoint(r)]
}

// icebergEndpoint names the Iceberg REST endpoint r calls, as
// "<METHOD> <path template>", or "" when r is not under icebergRoot. The
// prefix, a table bucket ARN, arrives percent-encoded, so the escaped path is
// read to keep it one segment.
func icebergEndpoint(r *http.Request) string {
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), icebergRoot+"/v1/")
	if !ok {
		return ""
	}
	if rest == "config" {
		return r.Method + " /v1/config"
	}
	segments := strings.Split(rest, "/")
	segments[0] = "{prefix}"
	if len(segments) > 2 && segments[1] == "namespaces" {
		segments[2] = "{namespace}"
		if len(segments) > 4 && segments[3] == "tables" {
			segments[4] = "{table}"
		}
	}
	return r.Method + " /v1/" + strings.Join(segments, "/")
}
