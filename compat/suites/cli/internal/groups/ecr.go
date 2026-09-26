package groups

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/overcast-sh/overcast-compat-cli/internal/awscli"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// ECR returns the Elastic Container Registry service group.
//
// ECR is awsJson1_1, so every operation is a POST to "/" carrying an
// X-Amz-Target header — the same URI S3 serves its service root on. Nothing
// about the path distinguishes the two: dispatch is the target header, and
// after that the SigV4 credential scope. That makes ECR a member of the
// wrong-service-answers family behind issue #963 even though the
// trailing-slash URI shape that started it cannot apply here, so
// DescribeRepositoriesSigned drives the same read a second time with a signed
// request and asserts it still lands on ECR.
//
// Groups: ecr-repositories, ecr-images. ecr-registry, ecr-policies and
// ecr-tags resolve through their authored scenarios
// (compat/model/authored/ecr-registry.json, ecr-policies.json, ecr-tags.json).
func ECR() ServiceGroup {
	g := &ecrGroup{}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			// ecr-repositories
			"ecr-repositories:CreateRepository":              g.CreateRepository,
			"ecr-repositories:DescribeRepositories":          g.DescribeRepositories,
			"ecr-repositories:DescribeRepositoriesAll":       g.DescribeRepositoriesAll,
			"ecr-repositories:DescribeRepositoriesPaginated": g.DescribeRepositoriesPaginated,
			"ecr-repositories:DescribeRepositoriesSigned":    g.DescribeRepositoriesSigned,
			"ecr-repositories:DescribeRepositoriesNotFound":  g.DescribeRepositoriesNotFound,
			"ecr-repositories:CreateRepositoryAlreadyExists": g.CreateRepositoryAlreadyExists,
			"ecr-repositories:DeleteRepository":              g.DeleteRepository,
			"ecr-repositories:DeleteRepositoryNotEmpty":      g.DeleteRepositoryNotEmpty,
			"ecr-repositories:DeleteRepositoryForce":         g.DeleteRepositoryForce,

			// ecr-images. EC2 models a DescribeImages of its own, so that key
			// is ambiguous bare and the loader refuses it.
			"ecr-images:PutImage":               g.PutImage,
			"ecr-images:ListImages":             g.ListImages,
			"ecr-images:ListImagesPaginated":    g.ListImagesPaginated,
			"ecr-images:DescribeImages":         g.DescribeImages,
			"ecr-images:BatchGetImage":          g.BatchGetImage,
			"ecr-images:BatchDeleteImage":       g.BatchDeleteImage,
			"ecr-images:DescribeImagesNotFound": g.DescribeImagesNotFound,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"ecr-repositories": g.setupRepositories,
			"ecr-images":       g.setupImages,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"ecr-repositories": g.teardownRepositories,
			"ecr-images":       g.teardownImages,
		},
	}
}

type ecrGroup struct{}

// One namer per group so no two groups ever contend for a repository name —
// CreateRepository answers RepositoryAlreadyExistsException on a repeat, and
// the groups run in parallel. ECR repository names are lowercase, which the
// run ID and these tags already are.
var (
	ecrReposNamer  = harness.NewNamer("ecr-repo")
	ecrImagesNamer = harness.NewNamer("ecr-img")
)

// Manifests are stored under the digest of their own bytes, so two images that
// differ only by tag collide on one record. Every image these groups publish
// therefore carries distinct manifest bytes.
func ecrManifest(id string) string {
	return fmt.Sprintf(
		`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json",`+
			`"config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":%d,`+
			`"digest":"sha256:%064s"},"layers":[]}`,
		len(id), id)
}

// ─── Shared helpers ───────────────────────────────────────────────────────────

// expectECRFailure runs a command that must fail and asserts both halves of the
// AWS error contract: the code the pinned model names, and the HTTP status it
// is bound to. ECR is awsJson1_1 and none of its error shapes carries an
// explicit httpStatusCode, so every one of them is the protocol's 400 default —
// asserting only the code would not notice a service that answered 404.
func expectECRFailure(t *harness.TestContext, testName, wantCode string, wantStatus int, args ...string) error {
	status, err := awscli.RunStatus(t.Endpoint, t.Region, args...)
	if err == nil {
		return fmt.Errorf("%s: expected %s, got success", testName, wantCode)
	}
	if !strings.Contains(err.Error(), wantCode) {
		return fmt.Errorf("%s: expected %s, got %v", testName, wantCode, err)
	}
	if status != wantStatus {
		return fmt.Errorf("%s: expected HTTP %d for %s, got %d", testName, wantStatus, wantCode, status)
	}
	return nil
}

// ecrRepositories pulls the repository list out of a DescribeRepositories
// response.
func ecrRepositories(out map[string]any) []map[string]any {
	raw, _ := out["repositories"].([]any)
	repos := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			repos = append(repos, m)
		}
	}
	return repos
}

// ecrRepoNames lists the repository names in a DescribeRepositories response.
func ecrRepoNames(out map[string]any) []string {
	repos := ecrRepositories(out)
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		if n, _ := r["repositoryName"].(string); n != "" {
			names = append(names, n)
		}
	}
	return names
}

// ecrFindRepo returns the named repository from a DescribeRepositories
// response, or nil when it is absent.
func ecrFindRepo(out map[string]any, name string) map[string]any {
	for _, r := range ecrRepositories(out) {
		if n, _ := r["repositoryName"].(string); n == name {
			return r
		}
	}
	return nil
}

// ecrCheckRepoShape asserts the fields every repository response carries: the
// name that was asked for, an ECR ARN ending in it, a registry URI naming it,
// and a creation time.
func ecrCheckRepoShape(testName, region, name string, repo map[string]any) error {
	if repo == nil {
		return fmt.Errorf("%s: no repository %q in the response", testName, name)
	}
	arn, _ := repo["repositoryArn"].(string)
	wantPrefix := "arn:aws:ecr:" + region + ":"
	if !strings.HasPrefix(arn, wantPrefix) || !strings.HasSuffix(arn, ":repository/"+name) {
		return fmt.Errorf("%s: expected an ARN %s…:repository/%s, got %q", testName, wantPrefix, name, arn)
	}
	if id, _ := repo["registryId"].(string); id == "" {
		return fmt.Errorf("%s: registryId is empty", testName)
	}
	// The URI's host is the registry container's published address, which is
	// not fixed — only the repository path within it is.
	uri, _ := repo["repositoryUri"].(string)
	if !strings.HasSuffix(uri, "/"+name) {
		return fmt.Errorf("%s: expected a repositoryUri ending in /%s, got %q", testName, name, uri)
	}
	if repo["createdAt"] == nil {
		return fmt.Errorf("%s: createdAt is absent", testName)
	}
	return nil
}

// ecrDeleteRepo removes a repository and everything in it. Teardown always
// forces: a repository holding an image is not deletable otherwise, which is
// the very contract DeleteRepositoryNotEmpty asserts.
func ecrDeleteRepo(t *harness.TestContext, name string) {
	if name == "" {
		return
	}
	awscli.Run(t.Endpoint, t.Region, "ecr", "delete-repository", "--repository-name", name, "--force") //nolint:errcheck
}

// ecrCreateRepo creates a repository and returns its ARN.
func ecrCreateRepo(t *harness.TestContext, testName, name string) (string, error) {
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "ecr", "create-repository", "--repository-name", name)
	if err != nil {
		return "", fmt.Errorf("%s: create-repository %s: %w", testName, name, err)
	}
	repo, _ := out["repository"].(map[string]any)
	arn, _ := repo["repositoryArn"].(string)
	if arn == "" {
		return "", fmt.Errorf("%s: create-repository %s returned no repositoryArn", testName, name)
	}
	return arn, nil
}

// ecrPutImage publishes one image and returns the digest ECR assigned it.
func ecrPutImage(t *harness.TestContext, testName, repo, tag, manifestID string) (string, error) {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"ecr", "put-image",
		"--repository-name", repo,
		"--image-tag", tag,
		"--image-manifest", ecrManifest(manifestID),
	)
	if err != nil {
		return "", fmt.Errorf("%s: put-image %s:%s: %w", testName, repo, tag, err)
	}
	img, _ := out["image"].(map[string]any)
	id, _ := img["imageId"].(map[string]any)
	digest, _ := id["imageDigest"].(string)
	if digest == "" {
		return "", fmt.Errorf("%s: put-image %s:%s returned no imageDigest", testName, repo, tag)
	}
	return digest, nil
}

// ecrImageTags lists the tags ListImages reports for a repository.
func ecrImageTags(t *harness.TestContext, testName, repo string) ([]string, error) {
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "ecr", "list-images", "--repository-name", repo)
	if err != nil {
		return nil, fmt.Errorf("%s: list-images %s: %w", testName, repo, err)
	}
	raw, _ := out["imageIds"].([]any)
	tags := make([]string, 0, len(raw))
	for _, r := range raw {
		m, _ := r.(map[string]any)
		if tag, _ := m["imageTag"].(string); tag != "" {
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	return tags, nil
}

func ecrContains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// ─── ecr-repositories ─────────────────────────────────────────────────────────

// The repositories group owns exactly these five names, all derived from the
// run ID, and teardown removes exactly these five.
func (g *ecrGroup) repoName(t *harness.TestContext) string { return ecrReposNamer.Name(t) }
func (g *ecrGroup) repoPageName(t *harness.TestContext, n string) string {
	return ecrReposNamer.Suffixed(t, "-page"+n)
}
func (g *ecrGroup) repoDeleteName(t *harness.TestContext) string {
	return ecrReposNamer.Suffixed(t, "-del")
}
func (g *ecrGroup) repoNotEmptyName(t *harness.TestContext) string {
	return ecrReposNamer.Suffixed(t, "-nonempty")
}
func (g *ecrGroup) repoForceName(t *harness.TestContext) string {
	return ecrReposNamer.Suffixed(t, "-force")
}

// setupRepositories creates the two extra repositories the pagination test
// needs alongside the one CreateRepository makes, so that at least three exist
// however the rest of the group fares.
func (g *ecrGroup) setupRepositories(_ context.Context, t *harness.TestContext) error {
	for _, n := range []string{"1", "2"} {
		if _, err := ecrCreateRepo(t, "setupRepositories", g.repoPageName(t, n)); err != nil {
			return err
		}
	}
	return nil
}

func (g *ecrGroup) teardownRepositories(_ context.Context, t *harness.TestContext) error {
	for _, name := range []string{
		g.repoForceName(t),
		g.repoNotEmptyName(t),
		g.repoDeleteName(t),
		g.repoName(t),
		g.repoPageName(t, "2"),
		g.repoPageName(t, "1"),
	} {
		ecrDeleteRepo(t, name)
	}
	return nil
}

func (g *ecrGroup) CreateRepository(_ context.Context, t *harness.TestContext) error {
	name := g.repoName(t)
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"ecr", "create-repository", "--repository-name", name)
	if err != nil {
		return fmt.Errorf("ecr CreateRepository: %w", err)
	}
	repo, _ := out["repository"].(map[string]any)
	if err := ecrCheckRepoShape("ecr CreateRepository", t.Region, name, repo); err != nil {
		return err
	}
	if m, _ := repo["imageTagMutability"].(string); m != "MUTABLE" {
		return fmt.Errorf("ecr CreateRepository: expected imageTagMutability MUTABLE, got %v", repo["imageTagMutability"])
	}

	// Round-trip: the repository the create call described must be the one the
	// registry now serves.
	got, err := awscli.RunOutput(t.Endpoint, t.Region,
		"ecr", "describe-repositories", "--repository-names", name)
	if err != nil {
		return fmt.Errorf("ecr CreateRepository: describe after create: %w", err)
	}
	described := ecrFindRepo(got, name)
	if err := ecrCheckRepoShape("ecr CreateRepository", t.Region, name, described); err != nil {
		return err
	}
	if described["repositoryArn"] != repo["repositoryArn"] {
		return fmt.Errorf("ecr CreateRepository: create returned ARN %v, describe returned %v",
			repo["repositoryArn"], described["repositoryArn"])
	}
	return nil
}

func (g *ecrGroup) DescribeRepositories(_ context.Context, t *harness.TestContext) error {
	name := g.repoName(t)
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"ecr", "describe-repositories", "--repository-names", name)
	if err != nil {
		return fmt.Errorf("ecr DescribeRepositories: %w", err)
	}
	repos := ecrRepositories(out)
	if len(repos) != 1 {
		return fmt.Errorf("ecr DescribeRepositories: --repository-names %s returned %d repositories, want 1", name, len(repos))
	}
	return ecrCheckRepoShape("ecr DescribeRepositories", t.Region, name, repos[0])
}

// DescribeRepositoriesAll exercises the unfiltered collection read — the shape
// of call that #963 found answering 501, or worse answering as another service.
func (g *ecrGroup) DescribeRepositoriesAll(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "ecr", "describe-repositories")
	if err != nil {
		return fmt.Errorf("ecr DescribeRepositoriesAll: %w", err)
	}
	repos := ecrRepositories(out)
	if len(repos) == 0 {
		return fmt.Errorf("ecr DescribeRepositoriesAll: the unfiltered list is empty, but this group created repositories")
	}
	for _, want := range []string{g.repoName(t), g.repoPageName(t, "1"), g.repoPageName(t, "2")} {
		if err := ecrCheckRepoShape("ecr DescribeRepositoriesAll", t.Region, want, ecrFindRepo(out, want)); err != nil {
			return err
		}
	}
	return nil
}

// DescribeRepositoriesPaginated drives the CLI's paginator over the collection.
//
// Two things are asserted, both independent of what other groups are creating
// in parallel: a full walk at --page-size 1 still returns every repository this
// group owns, and --max-items truncates to exactly the requested count while
// handing back a resumption token. A service that answered a page and then lost
// the rest, or that emitted a token the paginator could not terminate on, fails
// the first; a collection route that is not bound at all fails both.
func (g *ecrGroup) DescribeRepositoriesPaginated(_ context.Context, t *harness.TestContext) error {
	walked, err := awscli.RunOutput(t.Endpoint, t.Region,
		"ecr", "describe-repositories", "--page-size", "1")
	if err != nil {
		return fmt.Errorf("ecr DescribeRepositoriesPaginated: full walk at --page-size 1: %w", err)
	}
	names := ecrRepoNames(walked)
	for _, want := range []string{g.repoName(t), g.repoPageName(t, "1"), g.repoPageName(t, "2")} {
		if !ecrContains(names, want) {
			return fmt.Errorf("ecr DescribeRepositoriesPaginated: walking at --page-size 1 never returned %q (got %d repositories)", want, len(names))
		}
	}

	first, err := awscli.RunOutput(t.Endpoint, t.Region,
		"ecr", "describe-repositories", "--page-size", "1", "--max-items", "1")
	if err != nil {
		return fmt.Errorf("ecr DescribeRepositoriesPaginated: first page: %w", err)
	}
	if got := len(ecrRepositories(first)); got != 1 {
		return fmt.Errorf("ecr DescribeRepositoriesPaginated: --max-items 1 returned %d repositories, want 1", got)
	}
	token, _ := first["NextToken"].(string)
	if token == "" {
		return fmt.Errorf("ecr DescribeRepositoriesPaginated: --max-items 1 returned no NextToken, but this group owns 3 repositories")
	}

	// Resuming must make progress rather than replay the first page: at most
	// one repository is skipped, so at least two of the three are still owed.
	resumed, err := awscli.RunOutput(t.Endpoint, t.Region,
		"ecr", "describe-repositories", "--starting-token", token)
	if err != nil {
		return fmt.Errorf("ecr DescribeRepositoriesPaginated: resuming from the NextToken: %w", err)
	}
	rest := ecrRepoNames(resumed)
	owed := 0
	for _, want := range []string{g.repoName(t), g.repoPageName(t, "1"), g.repoPageName(t, "2")} {
		if ecrContains(rest, want) {
			owed++
		}
	}
	if owed < 2 {
		return fmt.Errorf("ecr DescribeRepositoriesPaginated: resuming returned only %d of this group's 3 repositories (%d in total)", owed, len(rest))
	}
	return nil
}

// DescribeRepositoriesSigned repeats the read with a SigV4-signed request.
//
// Every ECR operation posts to "/", which is also S3's service root, so the
// only things standing between an ECR call and another service's handler are
// the X-Amz-Target header and the credential scope. The rest of this suite
// calls unsigned, which no production client does; this test proves the signed
// path a real caller uses reaches ECR and answers with ECR's shape.
func (g *ecrGroup) DescribeRepositoriesSigned(_ context.Context, t *harness.TestContext) error {
	name := g.repoName(t)
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region,
		"ecr", "describe-repositories", "--repository-names", name)
	if err != nil {
		return fmt.Errorf("ecr DescribeRepositoriesSigned: %w", err)
	}
	if _, isECR := out["repositories"]; !isECR {
		return fmt.Errorf("ecr DescribeRepositoriesSigned: the signed response has no \"repositories\" key — it was answered by something other than ECR: %v", out)
	}
	return ecrCheckRepoShape("ecr DescribeRepositoriesSigned", t.Region, name, ecrFindRepo(out, name))
}

func (g *ecrGroup) DescribeRepositoriesNotFound(_ context.Context, t *harness.TestContext) error {
	return expectECRFailure(t, "ecr DescribeRepositoriesNotFound", "RepositoryNotFoundException", 400,
		"ecr", "describe-repositories", "--repository-names", g.repoName(t)+"-absent")
}

func (g *ecrGroup) CreateRepositoryAlreadyExists(_ context.Context, t *harness.TestContext) error {
	return expectECRFailure(t, "ecr CreateRepositoryAlreadyExists", "RepositoryAlreadyExistsException", 400,
		"ecr", "create-repository", "--repository-name", g.repoName(t))
}

func (g *ecrGroup) DeleteRepository(_ context.Context, t *harness.TestContext) error {
	name := g.repoDeleteName(t)
	if _, err := ecrCreateRepo(t, "ecr DeleteRepository", name); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"ecr", "delete-repository", "--repository-name", name)
	if err != nil {
		return fmt.Errorf("ecr DeleteRepository: %w", err)
	}
	repo, _ := out["repository"].(map[string]any)
	if n, _ := repo["repositoryName"].(string); n != name {
		return fmt.Errorf("ecr DeleteRepository: expected the deleted repository %q in the response, got %v", name, repo["repositoryName"])
	}
	return expectECRFailure(t, "ecr DeleteRepository", "RepositoryNotFoundException", 400,
		"ecr", "describe-repositories", "--repository-names", name)
}

// DeleteRepositoryNotEmpty asserts the guard AWS puts on a destructive call:
// per the ECR API Reference, DeleteRepository on a repository that still holds
// images answers RepositoryNotEmptyException unless force is set. It is the
// reason --force exists, and a caller that relies on the guard — a stack
// teardown expecting to be told rather than to silently lose the images — has
// no other signal.
func (g *ecrGroup) DeleteRepositoryNotEmpty(_ context.Context, t *harness.TestContext) error {
	name := g.repoNotEmptyName(t)
	if _, err := ecrCreateRepo(t, "ecr DeleteRepositoryNotEmpty", name); err != nil {
		return err
	}
	if _, err := ecrPutImage(t, "ecr DeleteRepositoryNotEmpty", name, "keepme", "notempty"); err != nil {
		return err
	}
	if err := expectECRFailure(t, "ecr DeleteRepositoryNotEmpty", "RepositoryNotEmptyException", 400,
		"ecr", "delete-repository", "--repository-name", name); err != nil {
		return err
	}
	// The refusal must also have left the repository alone.
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"ecr", "describe-repositories", "--repository-names", name)
	if err != nil {
		return fmt.Errorf("ecr DeleteRepositoryNotEmpty: the repository is gone after a refused delete: %w", err)
	}
	return ecrCheckRepoShape("ecr DeleteRepositoryNotEmpty", t.Region, name, ecrFindRepo(out, name))
}

func (g *ecrGroup) DeleteRepositoryForce(_ context.Context, t *harness.TestContext) error {
	name := g.repoForceName(t)
	if _, err := ecrCreateRepo(t, "ecr DeleteRepositoryForce", name); err != nil {
		return err
	}
	if _, err := ecrPutImage(t, "ecr DeleteRepositoryForce", name, "doomed", "force"); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"ecr", "delete-repository", "--repository-name", name, "--force")
	if err != nil {
		return fmt.Errorf("ecr DeleteRepositoryForce: %w", err)
	}
	repo, _ := out["repository"].(map[string]any)
	if n, _ := repo["repositoryName"].(string); n != name {
		return fmt.Errorf("ecr DeleteRepositoryForce: expected the deleted repository %q in the response, got %v", name, repo["repositoryName"])
	}
	return expectECRFailure(t, "ecr DeleteRepositoryForce", "RepositoryNotFoundException", 400,
		"ecr", "describe-repositories", "--repository-names", name)
}

// ─── ecr-images ───────────────────────────────────────────────────────────────

func (g *ecrGroup) imagesRepo(t *harness.TestContext) string { return ecrImagesNamer.Name(t) }
func (g *ecrGroup) imagesPagedRepo(t *harness.TestContext) string {
	return ecrImagesNamer.Suffixed(t, "-paged")
}

// setupImages builds the two repositories the image tests publish into. The
// paginated test gets its own so that its page boundaries do not move when a
// neighbouring test adds or deletes an image.
func (g *ecrGroup) setupImages(_ context.Context, t *harness.TestContext) error {
	for _, name := range []string{g.imagesRepo(t), g.imagesPagedRepo(t)} {
		if _, err := ecrCreateRepo(t, "setupImages", name); err != nil {
			return err
		}
	}
	return nil
}

func (g *ecrGroup) teardownImages(_ context.Context, t *harness.TestContext) error {
	ecrDeleteRepo(t, g.imagesPagedRepo(t))
	ecrDeleteRepo(t, g.imagesRepo(t))
	return nil
}

func (g *ecrGroup) PutImage(_ context.Context, t *harness.TestContext) error {
	repo := g.imagesRepo(t)
	digest, err := ecrPutImage(t, "ecr PutImage", repo, "v1", "v1")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(digest, "sha256:") {
		return fmt.Errorf("ecr PutImage: expected a sha256 imageDigest, got %q", digest)
	}
	t.Set("_ecrImageDigest", digest)

	tags, err := ecrImageTags(t, "ecr PutImage", repo)
	if err != nil {
		return err
	}
	if !ecrContains(tags, "v1") {
		return fmt.Errorf("ecr PutImage: list-images does not show the pushed tag v1, got %v", tags)
	}
	return nil
}

func (g *ecrGroup) ListImages(_ context.Context, t *harness.TestContext) error {
	repo := g.imagesRepo(t)
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "ecr", "list-images", "--repository-name", repo)
	if err != nil {
		return fmt.Errorf("ecr ListImages: %w", err)
	}
	ids, _ := out["imageIds"].([]any)
	if len(ids) == 0 {
		return fmt.Errorf("ecr ListImages: no imageIds for %s, but PutImage published one", repo)
	}
	want := t.GetString("_ecrImageDigest")
	for _, raw := range ids {
		id, _ := raw.(map[string]any)
		if d, _ := id["imageDigest"].(string); d == want {
			if tag, _ := id["imageTag"].(string); tag != "v1" {
				return fmt.Errorf("ecr ListImages: expected tag v1 on %s, got %v", want, id["imageTag"])
			}
			return nil
		}
	}
	return fmt.Errorf("ecr ListImages: %s is not in the listing for %s", want, repo)
}

// ListImagesPaginated drives the paginator over a collection this group owns
// outright, so the page boundaries are deterministic: three images in, one out
// on the first page, the other two on the resumed page and no repeats.
func (g *ecrGroup) ListImagesPaginated(_ context.Context, t *harness.TestContext) error {
	repo := g.imagesPagedRepo(t)
	for _, tag := range []string{"page1", "page2", "page3"} {
		if _, err := ecrPutImage(t, "ecr ListImagesPaginated", repo, tag, tag); err != nil {
			return err
		}
	}

	first, err := awscli.RunOutput(t.Endpoint, t.Region,
		"ecr", "list-images", "--repository-name", repo, "--page-size", "1", "--max-items", "1")
	if err != nil {
		return fmt.Errorf("ecr ListImagesPaginated: first page: %w", err)
	}
	ids, _ := first["imageIds"].([]any)
	if len(ids) != 1 {
		return fmt.Errorf("ecr ListImagesPaginated: --max-items 1 returned %d imageIds, want 1", len(ids))
	}
	firstID, _ := ids[0].(map[string]any)
	firstTag, _ := firstID["imageTag"].(string)
	token, _ := first["NextToken"].(string)
	if token == "" {
		return fmt.Errorf("ecr ListImagesPaginated: --max-items 1 returned no NextToken, but %s holds 3 images", repo)
	}

	resumed, err := awscli.RunOutput(t.Endpoint, t.Region,
		"ecr", "list-images", "--repository-name", repo, "--starting-token", token)
	if err != nil {
		return fmt.Errorf("ecr ListImagesPaginated: resuming from the NextToken: %w", err)
	}
	rest, _ := resumed["imageIds"].([]any)
	if len(rest) != 2 {
		return fmt.Errorf("ecr ListImagesPaginated: resuming returned %d imageIds, want the 2 the first page did not", len(rest))
	}
	for _, raw := range rest {
		id, _ := raw.(map[string]any)
		if tag, _ := id["imageTag"].(string); tag == firstTag {
			return fmt.Errorf("ecr ListImagesPaginated: resuming replayed %q from the first page", firstTag)
		}
	}
	return nil
}

func (g *ecrGroup) DescribeImages(_ context.Context, t *harness.TestContext) error {
	repo := g.imagesRepo(t)
	want := t.GetString("_ecrImageDigest")
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"ecr", "describe-images", "--repository-name", repo, "--image-ids", "imageTag=v1")
	if err != nil {
		return fmt.Errorf("ecr DescribeImages: %w", err)
	}
	details, _ := out["imageDetails"].([]any)
	if len(details) != 1 {
		return fmt.Errorf("ecr DescribeImages: expected 1 imageDetail for tag v1, got %d", len(details))
	}
	d, _ := details[0].(map[string]any)
	if digest, _ := d["imageDigest"].(string); digest != want {
		return fmt.Errorf("ecr DescribeImages: expected imageDigest %q, got %v", want, d["imageDigest"])
	}
	if name, _ := d["repositoryName"].(string); name != repo {
		return fmt.Errorf("ecr DescribeImages: expected repositoryName %q, got %v", repo, d["repositoryName"])
	}
	rawTags, _ := d["imageTags"].([]any)
	tags := make([]string, 0, len(rawTags))
	for _, rt := range rawTags {
		if s, ok := rt.(string); ok {
			tags = append(tags, s)
		}
	}
	if !ecrContains(tags, "v1") {
		return fmt.Errorf("ecr DescribeImages: expected imageTags to contain v1, got %v", tags)
	}
	return nil
}

func (g *ecrGroup) BatchGetImage(_ context.Context, t *harness.TestContext) error {
	repo := g.imagesRepo(t)
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"ecr", "batch-get-image", "--repository-name", repo, "--image-ids", "imageTag=v1")
	if err != nil {
		return fmt.Errorf("ecr BatchGetImage: %w", err)
	}
	if failures, _ := out["failures"].([]any); len(failures) != 0 {
		return fmt.Errorf("ecr BatchGetImage: expected no failures for tag v1, got %v", failures)
	}
	images, _ := out["images"].([]any)
	if len(images) != 1 {
		return fmt.Errorf("ecr BatchGetImage: expected 1 image for tag v1, got %d", len(images))
	}
	img, _ := images[0].(map[string]any)
	if manifest, _ := img["imageManifest"].(string); manifest != ecrManifest("v1") {
		return fmt.Errorf("ecr BatchGetImage: the manifest read back is not the one PutImage stored: %v", img["imageManifest"])
	}
	id, _ := img["imageId"].(map[string]any)
	if digest, _ := id["imageDigest"].(string); digest != t.GetString("_ecrImageDigest") {
		return fmt.Errorf("ecr BatchGetImage: expected imageDigest %q, got %v", t.GetString("_ecrImageDigest"), id["imageDigest"])
	}
	return nil
}

func (g *ecrGroup) BatchDeleteImage(_ context.Context, t *harness.TestContext) error {
	repo := g.imagesRepo(t)
	if _, err := ecrPutImage(t, "ecr BatchDeleteImage", repo, "doomed", "doomed"); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"ecr", "batch-delete-image", "--repository-name", repo, "--image-ids", "imageTag=doomed")
	if err != nil {
		return fmt.Errorf("ecr BatchDeleteImage: %w", err)
	}
	if failures, _ := out["failures"].([]any); len(failures) != 0 {
		return fmt.Errorf("ecr BatchDeleteImage: expected no failures, got %v", failures)
	}
	deleted, _ := out["imageIds"].([]any)
	if len(deleted) != 1 {
		return fmt.Errorf("ecr BatchDeleteImage: expected 1 deleted imageId, got %d", len(deleted))
	}

	tags, err := ecrImageTags(t, "ecr BatchDeleteImage", repo)
	if err != nil {
		return err
	}
	if ecrContains(tags, "doomed") {
		return fmt.Errorf("ecr BatchDeleteImage: the deleted tag is still listed: %v", tags)
	}
	if !ecrContains(tags, "v1") {
		return fmt.Errorf("ecr BatchDeleteImage: deleting \"doomed\" also removed v1: %v", tags)
	}
	return nil
}

func (g *ecrGroup) DescribeImagesNotFound(_ context.Context, t *harness.TestContext) error {
	return expectECRFailure(t, "ecr DescribeImagesNotFound", "ImageNotFoundException", 400,
		"ecr", "describe-images", "--repository-name", g.imagesRepo(t), "--image-ids", "imageTag=no-such-tag")
}
