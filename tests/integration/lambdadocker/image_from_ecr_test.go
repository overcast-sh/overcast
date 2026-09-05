package lambdadocker_test

// image_from_ecr_test.go — AWS's own container-image deployment pattern, whole.
//
// AWS's pattern is: build on a Lambda base image, push it to ECR, then create
// the function with PackageType=Image and Code.ImageUri naming that repository.
// The URI a client writes is
// "{account}.dkr.ecr.{region}.amazonaws.com/{repo}:{tag}" — CDK builds it from
// AWS::AccountId and AWS::Region rather than reading repositoryUri back — while
// the bytes are in the registry Overcast serves at localhost. Resolving one
// address to the other is what makes the pattern work locally, and pulling the
// URI as written instead leaves the machine and is refused ("no basic auth
// credentials").
//
// Both halves are covered on their own: the ECR suite pushes an image and pulls
// it back, and lambda_init_test.go invokes an image function from a
// locally-built tag. Neither joins them, so nothing failed when the join did.
// This test is the join, from `docker push` to the handler's own answer.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ecrOp calls one ECR operation on the test server.
func ecrOp(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal %s body: %v", operation, err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("build %s request: %v", operation, err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonEC2ContainerRegistry_V20150921."+operation)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ecr %s: %v", operation, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ecr %s: expected 200, got %d: %s", operation, resp.StatusCode, raw)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s response: %v: %s", operation, err, raw)
	}
	return out
}

// ecrLoginOrSkip authenticates the Docker CLI with the token ECR issues, the
// way `aws ecr get-login-password | docker login` does.
func ecrLoginOrSkip(t *testing.T, srv *helpers.TestServer) {
	t.Helper()
	body := ecrOp(t, srv, "GetAuthorizationToken", map[string]any{})
	data, ok := body["authorizationData"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("expected one authorizationData entry, got %#v", body["authorizationData"])
	}
	entry, _ := data[0].(map[string]any)
	tokenB64, _ := entry["authorizationToken"].(string)
	proxy, _ := entry["proxyEndpoint"].(string)
	decoded, err := base64.StdEncoding.DecodeString(tokenB64)
	if err != nil {
		t.Fatalf("decode authorizationToken: %v", err)
	}
	user, password, found := strings.Cut(string(decoded), ":")
	if !found {
		t.Fatalf("unexpected decoded token format: %q", string(decoded))
	}
	helpers.DockerLoginOrSkip(t, proxy, user, password)
}

func dockerCLI(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// TestInvoke_imageFunction_pushedToECRAndAddressedAsAWS runs AWS's documented
// container-image pattern end to end.
func TestInvoke_imageFunction_pushedToECRAndAddressedAsAWS(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	const account = "000000000000"
	const region = "us-east-1"
	const repo = "lambda-image-pattern"

	srv := helpers.NewTestServer(t,
		helpers.WithLambdaDocker(),
		helpers.WithRegion(region),
		helpers.WithAccountID(account),
		// The daemon does the pushing and the pulling, and on Docker Desktop it
		// cannot reach an ephemeral publish — see WithECRRegistryPort.
		helpers.WithECRRegistryPort(helpers.ReserveTCPPort(t)),
	)

	// Given: a handler built on the Lambda base image, in this account's ECR.
	created := ecrOp(t, srv, "CreateRepository", map[string]any{"repositoryName": repo})
	repository, _ := created["repository"].(map[string]any)
	repoURI, _ := repository["repositoryUri"].(string)
	if repoURI == "" {
		t.Fatalf("CreateRepository returned no repositoryUri: %#v", created)
	}

	local := buildLambdaImage(t, `FROM public.ecr.aws/lambda/nodejs:20
COPY app.js /var/task/app.js
CMD ["app.handler"]
`, map[string]string{"app.js": `
exports.handler = async (event) => ({ from: "the pushed image", event });
`})

	ecrLoginOrSkip(t, srv)
	pushed := repoURI + ":v1"
	dockerCLI(t, "tag", local, pushed)
	t.Cleanup(func() {
		_ = exec.CommandContext(context.Background(), "docker", "image", "rm", "-f", pushed).Run()
	})
	dockerCLI(t, "push", pushed)

	// When: the function names it the way AWS and CDK write it — a real ECR
	// endpoint on amazonaws.com, not the localhost address it was pushed to.
	imageURI := fmt.Sprintf("%s.dkr.ecr.%s.amazonaws.com/%s:v1", account, region, repo)
	createImageFunction(t, srv, "ecr-image-fn", imageURI, nil)
	waitForFunctionActive(t, srv, "ecr-image-fn")

	// Then: the handler in the pushed image answers.
	resp := invokeFunction(t, srv, "ecr-image-fn", map[string]any{"ping": 1})
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	if errType := resp.Header.Get("X-Amz-Function-Error"); errType != "" {
		t.Fatalf("invoke reported %s: %s", errType, payload)
	}
	helpers.AssertStatus(t, resp, http.StatusOK)

	var out struct {
		From  string         `json:"from"`
		Event map[string]any `json:"event"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("decode invoke payload: %v: %s", err, payload)
	}
	if out.From != "the pushed image" {
		t.Errorf("the answer did not come from the pushed image: %s", payload)
	}
	if got, ok := out.Event["ping"].(float64); !ok || got != 1 {
		t.Errorf("the event did not reach the handler: %s", payload)
	}

	// And: UpdateFunctionCode moves it onto a second push of the same
	// repository — the redeploy half of the pattern, which has to invalidate
	// the warm environment holding the first image.
	second := buildLambdaImage(t, `FROM public.ecr.aws/lambda/nodejs:20
COPY app.js /var/task/app.js
CMD ["app.handler"]
`, map[string]string{"app.js": `
exports.handler = async () => ({ from: "the second push" });
`})
	pushedV2 := repoURI + ":v2"
	dockerCLI(t, "tag", second, pushedV2)
	t.Cleanup(func() {
		_ = exec.CommandContext(context.Background(), "docker", "image", "rm", "-f", pushedV2).Run()
	})
	dockerCLI(t, "push", pushedV2)

	updated := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/ecr-image-fn/code"), map[string]any{
		"ImageUri": fmt.Sprintf("%s.dkr.ecr.%s.amazonaws.com/%s:v2", account, region, repo),
	})
	helpers.AssertStatus(t, updated, http.StatusOK)
	updated.Body.Close()
	waitForFunctionActive(t, srv, "ecr-image-fn")

	// The pool retires the environment built from the old image asynchronously,
	// so the second answer is polled for rather than demanded on the first
	// invoke — what is under test is that it arrives, not how fast.
	deadline := time.Now().Add(90 * time.Second)
	var last []byte
	for time.Now().Before(deadline) {
		r := invokeFunction(t, srv, "ecr-image-fn", map[string]any{})
		last, _ = io.ReadAll(r.Body)
		r.Body.Close()
		if strings.Contains(string(last), "the second push") {
			return
		}
		time.Sleep(time.Second)
	}
	t.Errorf("the function still answers from the first image after UpdateFunctionCode: %s", last)
}
