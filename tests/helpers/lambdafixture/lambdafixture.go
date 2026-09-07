// Package lambdafixture holds the Lambda wire types and request helpers that
// both Lambda integration test packages need.
//
// The suite is split in two so the halves run concurrently under `go test
// ./...`: tests/integration/lambda drives the control plane over HTTP and
// starts no container, tests/integration/lambdadocker holds everything that
// needs a Docker daemon. Neither can see the other's unexported declarations,
// and copying them would be two bodies to keep in step, so the shapes and
// helpers both halves use live here — once.
//
// Only genuinely shared declarations belong here. A helper one half alone uses
// stays file-scoped in that half, as tests/AGENTS.md asks.
package lambdafixture

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ─── wire types for functions ────────────────────────────────────────────────

// CreateFunctionReq is the CreateFunction request body.
type CreateFunctionReq struct {
	FunctionName  string            `json:"FunctionName"`
	Runtime       string            `json:"Runtime,omitempty"`
	Handler       string            `json:"Handler,omitempty"`
	Role          string            `json:"Role"`
	Description   string            `json:"Description,omitempty"`
	Timeout       int               `json:"Timeout,omitempty"`
	MemorySize    int               `json:"MemorySize,omitempty"`
	Environment   *Env              `json:"Environment,omitempty"`
	Code          *FunctionCode     `json:"Code,omitempty"`
	PackageType   string            `json:"PackageType,omitempty"`
	Architectures []string          `json:"Architectures,omitempty"`
	Tags          map[string]string `json:"Tags,omitempty"`
	VpcConfig     *VPCConfigReq     `json:"VpcConfig,omitempty"`
	ImageConfig   *ImageConfigReq   `json:"ImageConfig,omitempty"`
}

// ImageConfigReq is the ImageConfig member of a CreateFunction request.
type ImageConfigReq struct {
	EntryPoint       []string `json:"EntryPoint,omitempty"`
	Command          []string `json:"Command,omitempty"`
	WorkingDirectory string   `json:"WorkingDirectory,omitempty"`
}

// VPCConfigReq is the VpcConfig member of a CreateFunction request.
type VPCConfigReq struct {
	SubnetIds        []string `json:"SubnetIds,omitempty"`
	SecurityGroupIds []string `json:"SecurityGroupIds,omitempty"`
}

// VPCConfigResp is the VpcConfig member of a function configuration response.
type VPCConfigResp struct {
	SubnetIds        []string `json:"SubnetIds"`
	SecurityGroupIds []string `json:"SecurityGroupIds"`
	VpcId            string   `json:"VpcId"`
}

// Env is the Environment member of a CreateFunction request and of a function
// configuration response.
type Env struct {
	Variables map[string]string `json:"Variables"`
}

// FunctionCode is the Code member of a CreateFunction request.
type FunctionCode struct {
	ZipFile  []byte `json:"ZipFile,omitempty"`
	ImageUri string `json:"ImageUri,omitempty"`
}

// FunctionConfiguration is the function configuration every function-shaped
// response carries.
type FunctionConfiguration struct {
	FunctionName        string               `json:"FunctionName"`
	FunctionArn         string               `json:"FunctionArn"`
	Runtime             string               `json:"Runtime"`
	Handler             string               `json:"Handler"`
	Role                string               `json:"Role"`
	Description         string               `json:"Description"`
	Timeout             int                  `json:"Timeout"`
	MemorySize          int                  `json:"MemorySize"`
	State               string               `json:"State"`
	CodeSize            int64                `json:"CodeSize"`
	LastModified        string               `json:"LastModified"`
	RevisionId          string               `json:"RevisionId"`
	PackageType         string               `json:"PackageType"`
	Architectures       []string             `json:"Architectures"`
	ImageUri            string               `json:"ImageUri,omitempty"`
	Environment         *Env                 `json:"Environment,omitempty"`
	VpcConfig           *VPCConfigResp       `json:"VpcConfig,omitempty"`
	ImageConfigResponse *ImageConfigResponse `json:"ImageConfigResponse,omitempty"`
}

// ImageConfigResp is the ImageConfig member of an ImageConfigResponse.
type ImageConfigResp struct {
	EntryPoint       []string `json:"EntryPoint,omitempty"`
	Command          []string `json:"Command,omitempty"`
	WorkingDirectory string   `json:"WorkingDirectory,omitempty"`
}

// ImageConfigResponse is the ImageConfigResponse member of a function
// configuration response.
type ImageConfigResponse struct {
	ImageConfig *ImageConfigResp `json:"ImageConfig,omitempty"`
}

// ─── wire types for layers ───────────────────────────────────────────────────

// PublishLayerVersionReq is the PublishLayerVersion request body.
type PublishLayerVersionReq struct {
	Description             string       `json:"Description,omitempty"`
	Content                 LayerContent `json:"Content"`
	CompatibleRuntimes      []string     `json:"CompatibleRuntimes,omitempty"`
	CompatibleArchitectures []string     `json:"CompatibleArchitectures,omitempty"`
}

// LayerContent is the Content member of a PublishLayerVersion request.
type LayerContent struct {
	ZipFile []byte `json:"ZipFile,omitempty"`
}

// LayerVersionResponse is the PublishLayerVersion/GetLayerVersion response.
type LayerVersionResponse struct {
	LayerVersionArn         string                  `json:"LayerVersionArn"`
	LayerArn                string                  `json:"LayerArn"`
	Version                 int64                   `json:"Version"`
	Description             string                  `json:"Description,omitempty"`
	CreatedDate             string                  `json:"CreatedDate"`
	CompatibleRuntimes      []string                `json:"CompatibleRuntimes,omitempty"`
	CompatibleArchitectures []string                `json:"CompatibleArchitectures,omitempty"`
	Content                 LayerVersionContentResp `json:"Content"`
}

// LayerVersionContentResp is the Content member of a layer version response.
type LayerVersionContentResp struct {
	CodeSize int64 `json:"CodeSize"`
}

// ─── wire types for function URLs ────────────────────────────────────────────

// FunctionURLConfigResp mirrors the CreateFunctionUrlConfig/GetFunctionUrlConfig/
// UpdateFunctionUrlConfig response shape.
type FunctionURLConfigResp struct {
	FunctionArn      string `json:"FunctionArn"`
	FunctionUrl      string `json:"FunctionUrl"`
	AuthType         string `json:"AuthType"`
	InvokeMode       string `json:"InvokeMode"`
	CreationTime     string `json:"CreationTime"`
	LastModifiedTime string `json:"LastModifiedTime"`
}

// ─── URLs ────────────────────────────────────────────────────────────────────

// URL builds a URL under the 2015-03-31 API version, which the functions API
// most of these tests drive lives on.
func URL(srv *helpers.TestServer, path string) string {
	return srv.URL + "/2015-03-31" + path
}

// LayerURL builds a URL under the 2018-10-31 API version, which the layers API
// lives on.
func LayerURL(srv *helpers.TestServer, path string) string {
	return srv.URL + "/2018-10-31" + path
}

// URLv2021 builds a URL under the 2021-10-31 API version, used by
// function URL config endpoints (a different vintage than the 2015-03-31
// functions API most other helpers target).
func URLv2021(srv *helpers.TestServer, path string) string {
	return srv.URL + "/2021-10-31" + path
}

// StreamingURL builds the InvokeWithResponseStream URL for a function.
func StreamingURL(srv *helpers.TestServer, name string) string {
	return srv.URL + "/2021-11-15/functions/" + name + "/response-streaming-invocations"
}

// ─── requests ────────────────────────────────────────────────────────────────

// DoJSON sends a JSON request and returns the response, failing the test if the
// request could not be built or sent.
//
// The noctx exemption: this body used to live in a _test.go file, which
// .golangci.yml excludes from noctx, and it is unchanged. Giving it
// NewRequestWithContext(t.Context(), ...) would bind every one of its ~570 call
// sites to the test's lifetime — a cancellation change, which belongs in its
// own commit rather than in a package move.
func DoJSON(t *testing.T, method, url string, body any) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r) //nolint:noctx // see the note on DoJSON
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

// DecodeJSON decodes a response body into out and closes it.
//
// helpers.DecodeJSON is the general form and does almost the same thing; this
// one streams rather than buffering, and tolerates trailing bytes after the
// JSON value where the general form rejects them. Kept as it was so the split
// changes no assertion.
func DecodeJSON[T any](t *testing.T, resp *http.Response, out *T) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
}

// InvokeFunction sends a synchronous Lambda invocation and returns the HTTP response.
func InvokeFunction(t *testing.T, srv *helpers.TestServer, name string, payload any) *http.Response {
	t.Helper()
	return DoJSON(t, http.MethodPost, URL(srv, "/functions/"+name+"/invocations"), payload)
}
