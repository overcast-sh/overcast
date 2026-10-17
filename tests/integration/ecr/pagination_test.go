package ecr_test

// pagination_test.go — DescribeRepositories, ListImages and DescribeImages
// page their results, and the Delete*Policy operations hand back what they
// deleted (#2245).
//
// AWS ECR API Reference, DescribeRepositories / ListImages / DescribeImages:
// maxResults is 1..1000, "If this parameter is not used, then ... returns up to
// 100 results and a nextToken value, if applicable", and nextToken "should be
// treated as an opaque identifier". DescribeRepositories and DescribeImages
// also state that maxResults/nextToken "cannot be used when you specify"
// repositoryNames / imageIds. Each operation lists InvalidParameterException.

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func createRepos(t *testing.T, srv *helpers.TestServer, n int) []string {
	t.Helper()
	names := make([]string, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("repo-%03d", i)
		ecrCall(t, srv, "CreateRepository", map[string]any{"repositoryName": name}).Body.Close()
		names = append(names, name)
	}
	return names
}

func putImages(t *testing.T, srv *helpers.TestServer, repo string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		ecrCall(t, srv, "PutImage", map[string]any{
			"repositoryName": repo,
			"imageManifest":  fmt.Sprintf(`{"schemaVersion":2,"n":%d}`, i),
			"imageTag":       fmt.Sprintf("v%03d", i),
		}).Body.Close()
	}
}

func assertInvalidParameter(t *testing.T, resp *http.Response) {
	t.Helper()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	body := mustDecode(t, resp)
	if body["__type"] != "InvalidParameterException" {
		t.Fatalf("expected InvalidParameterException, got %v", body["__type"])
	}
}

// pageAll follows nextToken to the end and returns the page sizes and the
// value of key for every item under itemsKey.
func pageAll(t *testing.T, srv *helpers.TestServer, op string, base map[string]any, itemsKey, key string) (sizes []int, ids []string) {
	t.Helper()
	token := ""
	for guard := 0; guard < 50; guard++ {
		req := map[string]any{}
		for k, v := range base {
			req[k] = v
		}
		if token != "" {
			req["nextToken"] = token
		}
		resp := ecrCall(t, srv, op, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", op, resp.StatusCode)
		}
		body := mustDecode(t, resp)
		items, _ := body[itemsKey].([]any)
		sizes = append(sizes, len(items))
		for _, it := range items {
			ids = append(ids, fmt.Sprint(it.(map[string]any)[key]))
		}
		next, _ := body["nextToken"].(string)
		if next == "" {
			return sizes, ids
		}
		token = next
	}
	t.Fatalf("%s: pagination did not terminate", op)
	return nil, nil
}

func distinct(ids []string) int {
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	return len(seen)
}

func TestDescribeRepositories_maxResultsPagesWithNextToken(t *testing.T) {
	// Given: five repositories.
	srv := helpers.NewTestServer(t)
	names := createRepos(t, srv, 5)

	// When: they are read two at a time.
	sizes, ids := pageAll(t, srv, "DescribeRepositories", map[string]any{"maxResults": 2}, "repositories", "repositoryName")

	// Then: pages are bounded and together cover every repository once, in order.
	if fmt.Sprint(sizes) != "[2 2 1]" {
		t.Fatalf("page sizes = %v, want [2 2 1]", sizes)
	}
	if fmt.Sprint(ids) != fmt.Sprint(names) {
		t.Fatalf("repositories = %v, want %v", ids, names)
	}
}

func TestDescribeRepositories_lastPageHasNoNextToken(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createRepos(t, srv, 2)

	body := mustDecode(t, ecrCall(t, srv, "DescribeRepositories", map[string]any{"maxResults": 2}))

	if tok, present := body["nextToken"]; present && tok != nil && tok != "" {
		t.Fatalf("exact-fit page must not carry nextToken, got %v", tok)
	}
}

func TestDescribeRepositories_defaultPageIs100(t *testing.T) {
	// Given: 101 repositories and no maxResults.
	srv := helpers.NewTestServer(t)
	createRepos(t, srv, 101)

	// When/Then: AWS returns up to 100 and a nextToken.
	body := mustDecode(t, ecrCall(t, srv, "DescribeRepositories", map[string]any{}))
	if n := len(body["repositories"].([]any)); n != 100 {
		t.Fatalf("default page = %d repositories, want 100", n)
	}
	if tok, _ := body["nextToken"].(string); tok == "" {
		t.Fatal("expected nextToken after the 100-item default page")
	}
}

func TestDescribeRepositories_tokenIsStable(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createRepos(t, srv, 3)

	a := mustDecode(t, ecrCall(t, srv, "DescribeRepositories", map[string]any{"maxResults": 1}))
	b := mustDecode(t, ecrCall(t, srv, "DescribeRepositories", map[string]any{"maxResults": 1}))

	if a["nextToken"] == nil || a["nextToken"] != b["nextToken"] {
		t.Fatalf("same request must yield the same token: %v vs %v", a["nextToken"], b["nextToken"])
	}
}

func TestDescribeRepositories_invalidNextToken(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createRepos(t, srv, 2)

	resp := ecrCall(t, srv, "DescribeRepositories", map[string]any{"maxResults": 1, "nextToken": "not-a-token"})
	defer resp.Body.Close()

	assertInvalidParameter(t, resp)
}

func TestDescribeRepositories_maxResultsOutOfRange(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createRepos(t, srv, 1)
	for _, n := range []int{0, -1, 1001} {
		resp := ecrCall(t, srv, "DescribeRepositories", map[string]any{"maxResults": n})
		assertInvalidParameter(t, resp)
		resp.Body.Close()
	}
}

func TestDescribeRepositories_maxResultsWithRepositoryNames(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createRepos(t, srv, 2)

	resp := ecrCall(t, srv, "DescribeRepositories", map[string]any{"repositoryNames": []string{"repo-000"}, "maxResults": 1})
	defer resp.Body.Close()

	assertInvalidParameter(t, resp)
}

func TestListImages_maxResultsPagesWithNextToken(t *testing.T) {
	// Given: a repository with three tagged images.
	srv := helpers.NewTestServer(t)
	ecrCall(t, srv, "CreateRepository", map[string]any{"repositoryName": "paged"}).Body.Close()
	putImages(t, srv, "paged", 3)

	// When: listed one at a time.
	sizes, ids := pageAll(t, srv, "ListImages", map[string]any{"repositoryName": "paged", "maxResults": 1}, "imageIds", "imageTag")

	// Then: three one-item pages, every image once.
	if fmt.Sprint(sizes) != "[1 1 1]" {
		t.Fatalf("page sizes = %v, want [1 1 1]", sizes)
	}
	if distinct(ids) != 3 {
		t.Fatalf("images = %v, want 3 distinct", ids)
	}
}

func TestListImages_repeatRequestIsIdentical(t *testing.T) {
	srv := helpers.NewTestServer(t)
	ecrCall(t, srv, "CreateRepository", map[string]any{"repositoryName": "paged"}).Body.Close()
	putImages(t, srv, "paged", 3)
	req := map[string]any{"repositoryName": "paged", "maxResults": 1}

	a := mustDecode(t, ecrCall(t, srv, "ListImages", req))
	b := mustDecode(t, ecrCall(t, srv, "ListImages", req))

	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatalf("repeat request differs: %v vs %v", a, b)
	}
}

func TestListImages_invalidNextToken(t *testing.T) {
	srv := helpers.NewTestServer(t)
	ecrCall(t, srv, "CreateRepository", map[string]any{"repositoryName": "paged"}).Body.Close()

	resp := ecrCall(t, srv, "ListImages", map[string]any{"repositoryName": "paged", "nextToken": "bogus"})
	defer resp.Body.Close()

	assertInvalidParameter(t, resp)
}

func TestListImages_maxResultsOutOfRange(t *testing.T) {
	srv := helpers.NewTestServer(t)
	ecrCall(t, srv, "CreateRepository", map[string]any{"repositoryName": "paged"}).Body.Close()
	for _, n := range []int{0, 1001} {
		resp := ecrCall(t, srv, "ListImages", map[string]any{"repositoryName": "paged", "maxResults": n})
		assertInvalidParameter(t, resp)
		resp.Body.Close()
	}
}

func TestDescribeImages_maxResultsPagesWithNextToken(t *testing.T) {
	srv := helpers.NewTestServer(t)
	ecrCall(t, srv, "CreateRepository", map[string]any{"repositoryName": "paged"}).Body.Close()
	putImages(t, srv, "paged", 3)

	sizes, ids := pageAll(t, srv, "DescribeImages", map[string]any{"repositoryName": "paged", "maxResults": 2}, "imageDetails", "imageDigest")

	if fmt.Sprint(sizes) != "[2 1]" {
		t.Fatalf("page sizes = %v, want [2 1]", sizes)
	}
	if distinct(ids) != 3 {
		t.Fatalf("images = %v, want 3 distinct", ids)
	}
}

func TestDescribeImages_invalidNextToken(t *testing.T) {
	srv := helpers.NewTestServer(t)
	ecrCall(t, srv, "CreateRepository", map[string]any{"repositoryName": "paged"}).Body.Close()

	resp := ecrCall(t, srv, "DescribeImages", map[string]any{"repositoryName": "paged", "nextToken": "bogus"})
	defer resp.Body.Close()

	assertInvalidParameter(t, resp)
}

func TestDescribeImages_maxResultsWithImageIds(t *testing.T) {
	srv := helpers.NewTestServer(t)
	ecrCall(t, srv, "CreateRepository", map[string]any{"repositoryName": "paged"}).Body.Close()
	putImages(t, srv, "paged", 1)

	resp := ecrCall(t, srv, "DescribeImages", map[string]any{
		"repositoryName": "paged",
		"imageIds":       []map[string]any{{"imageTag": "v000"}},
		"maxResults":     1,
	})
	defer resp.Body.Close()

	assertInvalidParameter(t, resp)
}

func TestDeleteRepositoryPolicy_returnsDeletedPolicyText(t *testing.T) {
	srv := helpers.NewTestServer(t)
	ecrCall(t, srv, "CreateRepository", map[string]any{"repositoryName": "my-app"}).Body.Close()
	policy := `{"Version":"2012-10-17","Statement":[]}`
	ecrCall(t, srv, "SetRepositoryPolicy", map[string]any{"repositoryName": "my-app", "policyText": policy}).Body.Close()

	body := mustDecode(t, ecrCall(t, srv, "DeleteRepositoryPolicy", map[string]any{"repositoryName": "my-app"}))

	if body["policyText"] != policy {
		t.Fatalf("policyText = %v, want the deleted policy", body["policyText"])
	}
}

func TestDeleteLifecyclePolicy_returnsDeletedPolicyText(t *testing.T) {
	srv := helpers.NewTestServer(t)
	ecrCall(t, srv, "CreateRepository", map[string]any{"repositoryName": "my-app"}).Body.Close()
	lifecycle := `{"rules":[{"rulePriority":1,"selection":{"tagStatus":"any","countType":"imageCountMoreThan","countNumber":10},"action":{"type":"expire"}}]}`
	ecrCall(t, srv, "PutLifecyclePolicy", map[string]any{"repositoryName": "my-app", "lifecyclePolicyText": lifecycle}).Body.Close()

	body := mustDecode(t, ecrCall(t, srv, "DeleteLifecyclePolicy", map[string]any{"repositoryName": "my-app"}))

	if body["lifecyclePolicyText"] != lifecycle {
		t.Fatalf("lifecyclePolicyText = %v, want the deleted policy", body["lifecyclePolicyText"])
	}
}
