package s3route

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/awsapi"
)

func TestRefused(t *testing.T) {
	for _, tc := range []struct {
		method, target, want string
		refused              bool
	}{
		// A sub-resource on a method S3 serves it on none is refused, not
		// served as the plain operation of the route.
		{http.MethodDelete, "/b?versioning", "", true},
		{http.MethodPut, "/b?location", "", true},
		{http.MethodHead, "/b?acl", "", true},
		{http.MethodPost, "/b?acl", "", true},
		{http.MethodPut, "/b?list-type=2", "", true},
		{http.MethodDelete, "/b/k?acl", "", true},
		{http.MethodPut, "/b/k?attributes", "", true},
		{http.MethodGet, "/b/k?uploads", "", true},

		// Query members that select an operation on one method are ordinary
		// parameters on the others.
		{http.MethodGet, "/b/k?partNumber=1", "GetObject", false},
		{http.MethodHead, "/b/k?partNumber=1", "HeadObject", false},
		{http.MethodPut, "/b/k?uploadId=u", "PutObject", false},

		// A parameter S3 does not know is ignored.
		{http.MethodPut, "/b?metadata", "CreateBucket", false},
		{http.MethodDelete, "/b?metadata", "DeleteBucket", false},
		{http.MethodPut, "/b/k?rename", "PutObject", false},
		// So is a sub-resource of a bucket sent to an object.
		{http.MethodPut, "/b/k?versioning", "PutObject", false},

		// Nothing selected and nothing refused: a bucket POST.
		{http.MethodPost, "/b", "", false},
	} {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			// Given: a path-style request
			r := httptest.NewRequest(tc.method, tc.target, nil)

			// When: it is named, and checked for refusal
			got, refused := Operation(r), Refused(r)

			// Then: S3 serves it as want, or refuses it
			if got != tc.want || refused != tc.refused {
				t.Fatalf("%s %s: Operation = %q, Refused = %v; want %q, %v", tc.method, tc.target, got, refused, tc.want, tc.refused)
			}
		})
	}
}

// modelQuery is a modeled S3 URI's query without x-id: its literals, with
// their values.
func modelQuery(uri string) string {
	_, query, _ := strings.Cut(uri, "?")
	literals := strings.Split(query, "&")
	return strings.Join(slices.DeleteFunc(literals, func(literal string) bool {
		return literal == "" || strings.HasPrefix(literal, "x-id=")
	}), "&")
}

func TestRefused_everyModeledSubResourceOnAnUnmodeledMethod(t *testing.T) {
	// Given: the methods the model binds each bucket and object query on
	type binding struct {
		level level
		query string
	}
	methods := map[binding][]string{}
	awsapi.WalkOperations(func(op awsapi.Operation) bool {
		if op.Service == "s3" && strings.Contains(op.URI, "{Bucket}") && modelQuery(op.URI) != "" {
			b := binding{modelLevel(op.URI), modelQuery(op.URI)}
			methods[b] = append(methods[b], op.HTTPMethod)
		}
		return true
	})
	paths := map[level]string{levelBucket: "/b", levelObject: "/b/k"}

	for b, bound := range methods {
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodHead, http.MethodDelete, http.MethodPost} {
			if slices.Contains(bound, method) {
				continue
			}
			target := paths[b.level] + "?" + b.query
			t.Run(method+" "+target, func(t *testing.T) {
				// When: it is sent on a method the model binds it on none
				r := httptest.NewRequest(method, target, nil)

				// Then: S3 refuses it and names it no operation
				if got := Operation(r); got != "" || !Refused(r) {
					t.Fatalf("%s %s: Operation = %q, Refused = %v; want refused", method, target, got, Refused(r))
				}
			})
		}
	}
}
