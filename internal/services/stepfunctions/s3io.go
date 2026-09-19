package stepfunctions

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
)

// S3 access for a distributed Map's ItemReader and ResultWriter. Every call
// goes through Overcast's own router as a path-style REST request, so it
// reaches exactly the S3 handler an SDK call would. (The aws-sdk:s3
// integration is the generic, shape-driven one in sdk_integration.go.)

// s3Object is one ListObjectsV2 entry, in the shape Step Functions hands a
// state (and a distributed Map iteration).
type s3Object struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
}

func (o s3Object) toJSON() map[string]any {
	return map[string]any{
		"Key":          o.Key,
		"LastModified": o.LastModified,
		"Etag":         o.ETag,
		"Size":         float64(o.Size),
		"StorageClass": o.StorageClass,
	}
}

// s3Path builds a path-style object path.
func s3Path(bucket, key string) string {
	path := "/" + url.PathEscape(bucket)
	if key != "" {
		segments := strings.Split(key, "/")
		for i, segment := range segments {
			segments[i] = url.PathEscape(segment)
		}
		path += "/" + strings.Join(segments, "/")
	}
	return path
}

// s3Call issues one S3 REST request through the router.
func (in *interpreter) s3Call(ctx context.Context, method, path, contentType string, body []byte) (*httptest.ResponseRecorder, *stateError) {
	if in.handler.router == nil {
		return nil, newStateError(errRuntime, "Overcast's Step Functions service has no router wired, so it cannot reach S3")
	}
	req, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(body))
	if err != nil {
		return nil, newStateError(errRuntime, "%v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("X-Overcast-Region", in.region)
	rec := httptest.NewRecorder()
	in.handler.router.ServeHTTP(rec, req)
	if rec.Code >= 400 {
		code, message := decodeS3Error(rec)
		return nil, &stateError{name: "S3." + code, cause: message}
	}
	return rec, nil
}

// decodeS3Error reads S3's bare <Error> document. A HEAD response has no body,
// so its status alone names the error.
func decodeS3Error(rec *httptest.ResponseRecorder) (code, message string) {
	var payload struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if xml.Unmarshal(rec.Body.Bytes(), &payload) == nil && payload.Code != "" {
		return payload.Code, payload.Message
	}
	if rec.Code == http.StatusNotFound {
		return "NotFound", "Not Found"
	}
	return "S3Exception", strings.TrimSpace(rec.Body.String())
}

// s3GetObject returns an object's bytes and content type.
func (in *interpreter) s3GetObject(ctx context.Context, bucket, key string) ([]byte, http.Header, *stateError) {
	rec, serr := in.s3Call(ctx, http.MethodGet, s3Path(bucket, key), "", nil)
	if serr != nil {
		return nil, nil, serr
	}
	return rec.Body.Bytes(), rec.Header(), nil
}

// s3PutObject writes an object and returns its ETag.
func (in *interpreter) s3PutObject(ctx context.Context, bucket, key string, body []byte, contentType string) (string, *stateError) {
	rec, serr := in.s3Call(ctx, http.MethodPut, s3Path(bucket, key), contentType, body)
	if serr != nil {
		return "", serr
	}
	return rec.Header().Get("ETag"), nil
}

type listObjectsV2Result struct {
	Name                  string     `xml:"Name"`
	Prefix                string     `xml:"Prefix"`
	KeyCount              int        `xml:"KeyCount"`
	MaxKeys               int        `xml:"MaxKeys"`
	IsTruncated           bool       `xml:"IsTruncated"`
	NextContinuationToken string     `xml:"NextContinuationToken"`
	Contents              []s3Object `xml:"Contents"`
}

// s3ListObjectsV2 returns one ListObjectsV2 page.
func (in *interpreter) s3ListObjectsV2(ctx context.Context, bucket string, params url.Values) (*listObjectsV2Result, *stateError) {
	params.Set("list-type", "2")
	rec, serr := in.s3Call(ctx, http.MethodGet, s3Path(bucket, "")+"?"+params.Encode(), "", nil)
	if serr != nil {
		return nil, serr
	}
	var out listObjectsV2Result
	if err := xml.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		return nil, newStateError(errTaskFailed, "the S3 ListObjectsV2 response could not be decoded: %v", err)
	}
	return &out, nil
}

// s3ListAll lists every object under a prefix, following continuation tokens.
func (in *interpreter) s3ListAll(ctx context.Context, bucket, prefix string) ([]s3Object, *stateError) {
	var (
		all   []s3Object
		token string
	)
	for {
		params := url.Values{}
		if prefix != "" {
			params.Set("prefix", prefix)
		}
		if token != "" {
			params.Set("continuation-token", token)
		}
		page, serr := in.s3ListObjectsV2(ctx, bucket, params)
		if serr != nil {
			return nil, serr
		}
		all = append(all, page.Contents...)
		if !page.IsTruncated || page.NextContinuationToken == "" {
			return all, nil
		}
		token = page.NextContinuationToken
	}
}
