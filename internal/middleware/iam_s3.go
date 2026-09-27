package middleware

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/overcast-sh/overcast/internal/s3route"
)

// iamCheck is one action on one resource that a request must be allowed.
type iamCheck struct {
	action   string
	resource string
}

// requestIAMChecks lists what r must be allowed to be served as op: op's
// action on the resource r names, and, for S3, what else the operation
// reads or deletes, and for an Iceberg REST call, every action AWS checks.
func requestIAMChecks(r *http.Request, op iamOperation) []iamCheck {
	switch op.service {
	case "s3":
		return s3IAMChecks(r)
	case "s3tables":
		if actions := icebergIAMActions(r); len(actions) > 0 {
			resource := requestIAMResource(r, op)
			checks := make([]iamCheck, len(actions))
			for i, action := range actions {
				checks[i] = iamCheck{action: action, resource: resource}
			}
			return checks
		}
	}
	return []iamCheck{{action: op.action, resource: requestIAMResource(r, op)}}
}

// s3IAMChecks lists what AWS checks before serving the S3 operation r is:
//
//   - the operation's action on the bucket or object r addresses, or its
//     versioned action when r names a versionId;
//   - for DeleteObjects, the delete action on each object the body names,
//     rather than one check on the bucket: AWS authorises a bulk delete key
//     by key, and a policy scoped to "bucket/*" or to a prefix is written
//     for exactly that;
//   - for a copy, s3:GetObject (or s3:GetObjectVersion) on the source object
//     as well.
//
// AWS answers a DeleteObjects key it refuses with an AccessDenied entry in
// a 200 response and deletes the rest. Here one refused key refuses the
// whole request, before anything is deleted.
func s3IAMChecks(r *http.Request) []iamCheck {
	operation := s3route.Operation(r)
	if operation == "DeleteObjects" {
		if checks := s3DeleteObjectsChecks(r); len(checks) > 0 {
			return checks
		}
	}
	checks := []iamCheck{{
		action:   iamActionFor("s3", operation, r.URL.Query().Get("versionId") != ""),
		resource: requestS3IAMResource(r),
	}}
	if copySource := r.Header.Get(s3route.CopySourceHeader); copySource != "" && (operation == "CopyObject" || operation == "UploadPartCopy") {
		checks = append(checks, s3CopySourceCheck(copySource))
	}
	return checks
}

// deleteObjectsBody is the part of a DeleteObjects request body that names
// what it deletes.
type deleteObjectsBody struct {
	Objects []struct {
		Key       string `xml:"Key"`
		VersionID string `xml:"VersionId"`
	} `xml:"Object"`
}

// s3DeleteObjectsChecks is one delete check per object r's body names, or
// none when the body names none that can be read.
//
// It reads the prefix of the body S3's handler reads, s3route.MaxDeleteObjectsBody,
// and decodes it as the handler does, so it names exactly the keys S3
// deletes. A body that does not decode here does not decode there either:
// S3 answers MalformedXML and deletes nothing. The body is left for the
// handler to read in full.
func s3DeleteObjectsChecks(r *http.Request) []iamCheck {
	if r.Body == nil {
		return nil
	}
	head, err := io.ReadAll(io.LimitReader(r.Body, s3route.MaxDeleteObjectsBody))
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), r.Body), r.Body}
	var body deleteObjectsBody
	if err != nil || xml.Unmarshal(head, &body) != nil {
		return nil
	}
	bucket := requestS3IAMResource(r)
	checks := make([]iamCheck, 0, len(body.Objects))
	seen := make(map[iamCheck]bool, len(body.Objects))
	for _, object := range body.Objects {
		check := iamCheck{
			action:   iamActionFor("s3", "DeleteObjects", object.VersionID != ""),
			resource: bucket + "/" + object.Key,
		}
		if !seen[check] {
			seen[check] = true
			checks = append(checks, check)
		}
	}
	return checks
}

// s3CopySourceCheck is the read a copy makes of the object copySource names:
// "bucket/key" or "/bucket/key", percent-encoded, with an optional
// "?versionId=". An access point's object ARN is its own resource.
func s3CopySourceCheck(copySource string) iamCheck {
	source, query, _ := strings.Cut(strings.TrimPrefix(copySource, "/"), "?")
	if decoded, err := url.PathUnescape(source); err == nil {
		source = decoded
	}
	versioned := false
	if values, err := url.ParseQuery(query); err == nil {
		versioned = values.Get("versionId") != ""
	}
	if !strings.HasPrefix(source, "arn:") {
		source = "arn:aws:s3:::" + source
	}
	return iamCheck{action: iamActionFor("s3", "GetObject", versioned), resource: source}
}
