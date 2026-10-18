package lambdaweb

// provider_docker_harness.go — the provider's surface for tests outside this
// package.
//
// The Docker-gated integration tests live in tests/integration/lambdawebdocker,
// and the provider's own types (envSpec, codeSource, codeRef) are unexported
// contracts of the data plane. This file names just enough of them for a test
// in another package to start environments against a real daemon: aliases for
// the result types, a flat request, and a code source built from a function.
// Nothing here is used by Overcast itself.

import (
	"context"
	"errors"
	"time"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// Environment is a running execution environment (see environment).
type Environment = environment

// InitFailure is why an environment did not become ready (see initFailure).
type InitFailure = initFailure

// StartRequest is envSpec flattened to what a test sets.
type StartRequest struct {
	Region, Function, Endpoint string
	// Runtime is the revision's runtime, nodejs24.x if empty.
	Runtime string
	// Bucket, Key and VersionID name the package InitCodeFetcher serves.
	Bucket, Key, VersionID string
	Env                    map[string]string
	MemoryMB               int
	InitTimeout            time.Duration
	LogGroup, LogStream    string
	AppLogLevel            string
}

// InitCodeFetcher wires a package source that is a function of the pinned
// version, standing in for the S3-backed one.
func (p *DockerProvider) InitCodeFetcher(fetch func(ctx context.Context, bucket, key, versionID string) ([]byte, error)) {
	p.InitCode(fetcherSource{fetch})
}

// StartEnvironment is Start for a StartRequest.
func (p *DockerProvider) StartEnvironment(ctx context.Context, r StartRequest) (Environment, *InitFailure) {
	runtime := r.Runtime
	if runtime == "" {
		runtime = "nodejs24.x"
	}
	rev := &revision{Function: r.Function, Region: r.Region, ID: "test-revision"}
	rev.BuildConfig.RuntimeConfig.Runtime = runtime
	return p.Start(ctx, envSpec{
		Region:      r.Region,
		Function:    r.Function,
		Endpoint:    r.Endpoint,
		Revision:    rev,
		Code:        codeRef{Bucket: r.Bucket, Key: r.Key, VersionID: r.VersionID},
		Env:         r.Env,
		MemoryMB:    r.MemoryMB,
		InitTimeout: r.InitTimeout,
		LogGroup:    r.LogGroup,
		LogStream:   r.LogStream,
		AppLogLevel: r.AppLogLevel,
		SysLogLevel: "INFO",
	})
}

type fetcherSource struct {
	fetch func(ctx context.Context, bucket, key, versionID string) ([]byte, error)
}

func (f fetcherSource) Resolve(_ context.Context, ref codeRef) (codeRef, int64, *protocol.AWSError) {
	return ref, 0, nil
}

func (f fetcherSource) Fetch(ctx context.Context, ref codeRef) ([]byte, *protocol.AWSError) {
	data, err := f.fetch(ctx, ref.Bucket, ref.Key, ref.VersionID)
	if err != nil {
		code := "InternalError"
		if errors.Is(err, ErrNoSuchPackage) {
			code = "NoSuchKey"
		}
		return nil, &protocol.AWSError{Code: code, Message: err.Error(), HTTPStatus: 500}
	}
	return data, nil
}

// ErrNoSuchPackage is what a fetch function returns for a package that is gone.
var ErrNoSuchPackage = errors.New("no such package")
