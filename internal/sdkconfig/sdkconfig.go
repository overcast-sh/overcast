// Package sdkconfig builds AWS SDK for Go v2 configurations that talk to
// Overcast: over the network at an endpoint, as the CLI does, or in process
// through the emulator's own router, as the sample-dataset endpoint and the
// runtime MCP tools do. Either way the caller drives Overcast through its
// public AWS API, so its validation, defaults and events all apply.
package sdkconfig

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Overcast accepts any credentials; these are the ones `overcast env` prints.
const (
	accessKeyID     = "test"
	secretAccessKey = "test"
)

// inProcessEndpoint is the origin in-process requests carry. Nothing dials
// it; it only gives each request a well-formed URL and Host.
const inProcessEndpoint = "http://localhost"

// ForEndpoint is a configuration for the Overcast at endpoint. A nil client
// uses the SDK's default.
func ForEndpoint(endpoint, region string, client aws.HTTPClient) aws.Config {
	cfg := aws.Config{
		Region:       region,
		Credentials:  credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
		BaseEndpoint: aws.String(endpoint),
	}
	if client != nil {
		cfg.HTTPClient = client
	}
	return cfg
}

// InProcess is a configuration whose requests are served by h, the
// emulator's root router, without leaving the process.
func InProcess(h http.Handler, region string) aws.Config {
	return ForEndpoint(inProcessEndpoint, region, &http.Client{Transport: handlerTransport{h}})
}

// PathStyle makes an S3 client address buckets by path, which every Overcast
// endpoint serves; virtual-hosted names need DNS that may not exist.
func PathStyle(o *s3.Options) { o.UsePathStyle = true }

// handlerTransport answers a request by serving it with a handler.
type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		defer req.Body.Close()
	}
	in := req.Clone(detached{req.Context()})
	in.RequestURI = in.URL.RequestURI()
	in.RemoteAddr = "127.0.0.1:0"
	rec := httptest.NewRecorder()
	t.h.ServeHTTP(rec, in)
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

// detached is a request context that keeps its parent's cancellation and
// none of its values. The caller is usually a handler of the same router,
// and a request that carried the caller's router state (chi's route
// context, the caller's request id) would be routed as the caller.
// eventtarget.detachRouting replaces only chi's key because a delivery must
// keep the caller's region; an SDK request signs its own region, so nothing
// of the caller's context applies to it.
type detached struct{ parent context.Context }

func (d detached) Deadline() (time.Time, bool) { return d.parent.Deadline() }
func (d detached) Done() <-chan struct{}       { return d.parent.Done() }
func (d detached) Err() error                  { return d.parent.Err() }
func (d detached) Value(any) any               { return nil }
