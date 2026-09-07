package lambda_test

// The Lambda suite is two packages so its halves run concurrently: this one
// drives the control plane over HTTP, tests/integration/lambdadocker holds
// everything that starts a container. The wire types and request helpers both
// halves use live in tests/helpers/lambdafixture, once; this file binds them
// back to the short names the tests here already read with.
//
// Nothing but a binding belongs in this file. A helper only this half uses
// stays file-scoped where it is used.

import (
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers/lambdafixture"
)

type (
	createFunctionReq      = lambdafixture.CreateFunctionReq
	imageConfigReq         = lambdafixture.ImageConfigReq
	vpcConfigReq           = lambdafixture.VPCConfigReq
	lambdaCode             = lambdafixture.FunctionCode
	functionEnvironment    = lambdafixture.Env
	functionConfiguration  = lambdafixture.FunctionConfiguration
	imageConfigResponse    = lambdafixture.ImageConfigResponse
	publishLayerVersionReq = lambdafixture.PublishLayerVersionReq
	layerContent           = lambdafixture.LayerContent
	layerVersionResponse   = lambdafixture.LayerVersionResponse
	functionUrlConfigResp  = lambdafixture.FunctionURLConfigResp
)

var (
	lambdaURL      = lambdafixture.URL
	layerURL       = lambdafixture.LayerURL
	lambdaURLv2021 = lambdafixture.URLv2021
	streamingURL   = lambdafixture.StreamingURL
	doJSON         = lambdafixture.DoJSON
	invokeFunction = lambdafixture.InvokeFunction
)

// decodeJSON is generic, so it cannot be bound as a value like the others.
func decodeJSON[T any](t *testing.T, resp *http.Response, out *T) {
	t.Helper()
	lambdafixture.DecodeJSON(t, resp, out)
}
