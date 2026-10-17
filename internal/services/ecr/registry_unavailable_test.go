package ecr

// registry_unavailable_test.go — "the registry did not answer" is not
// "the repository is empty".
//
// Nothing tells Overcast about a `docker push`, so an image record exists only
// because syncRepoImagesFromRegistry swept the registry when a client asked
// what a repository holds. When that sweep cannot reach the registry it has
// nothing to add, and the store it leaves behind looks exactly like the store
// of a repository nobody ever pushed to.
//
// Answering ImageNotFoundException off that store is wrong twice over. Real ECR
// reports a server-side failure as ServerException, never as a missing image;
// and cdk-assets reads a missing image as "publish this again", so a registry
// that was merely unreachable costs a Docker build and a layer upload per asset
// while reporting nothing. The registry being down is a fact worth saying out
// loud.
//
// repoRegistryState already draws this distinction for the delete path — see
// its comment, "only the first is grounds for deleting anything". These tests
// hold the read paths to the same line.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// seedRepo writes the repository record the read paths look up before they
// sweep, so these tests exercise the sweep rather than RepositoryNotFound.
func seedRepo(t *testing.T, s *Service) {
	t.Helper()
	repo := &Repository{RepositoryName: syncRepo, RegistryId: s.accountID()}
	if err := s.saveRepo(context.Background(), syncRegion, repo); err != nil {
		t.Fatalf("seed repository: %v", err)
	}
}

// unreachableService wires a Service to a registry address that nothing is
// listening on, which is what a container that has not finished starting looks
// like from here.
func unreachableService(t *testing.T) *Service {
	t.Helper()
	// A closed httptest server hands back an address that is guaranteed
	// routable and guaranteed refused, without racing a port another test may
	// claim.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	return syncService(t, url)
}

func TestSyncRepoImagesFromRegistry_reportsUnavailableRatherThanEmpty(t *testing.T) {
	s := unreachableService(t)

	// Given: a record the sweep itself wrote on an earlier, successful sweep.
	seedImage(t, s, "sha256:aaa", "live", true)

	got := s.syncRepoImagesFromRegistry(context.Background(), syncRegion, syncRepo)

	if got != sweepUnavailable {
		t.Errorf("sweep = %v, want sweepUnavailable when the registry refuses the connection", got)
	}
	// And it must not have taken the record with it: an unanswered question is
	// not grounds for forgetting anything, which is the rule the delete path
	// already follows.
	if got := storedTags(t, s); len(got) != 1 {
		t.Errorf("stored images = %v, want the seeded record kept: the sweep forgot an image because it could not reach the registry", got)
	}
}

func TestSyncRepoImagesFromRegistry_reportsNotApplicableWithoutDocker(t *testing.T) {
	s := syncService(t, "http://127.0.0.1:1")
	s.docker = nil

	// A deployment with no Docker has no registry to be out of step with, so
	// the store is the only authority there has ever been and an empty answer
	// is the truth rather than an outage. This is the case that must NOT be
	// reported as unavailable, or every ECR call in a no-Docker run would
	// answer ServerException.
	if got := s.syncRepoImagesFromRegistry(context.Background(), syncRegion, syncRepo); got != sweepNotApplicable {
		t.Errorf("sweep = %v, want sweepNotApplicable when no Docker is wired", got)
	}
}

func TestDescribeImages_answersServerExceptionWhenTheRegistryIsUnreachable(t *testing.T) {
	s := unreachableService(t)
	seedRepo(t, s)

	_, aerr := s.describeImagesTyped(context.Background(), &describeImagesRequest{
		RepositoryName: syncRepo,
		ImageIds:       []ImageIdentifier{{ImageTag: "never-swept"}},
	})

	if aerr == nil {
		t.Fatal("describeImages succeeded against an unreachable registry")
	}
	if aerr.Code == "ImageNotFoundException" {
		t.Errorf("describeImages answered ImageNotFoundException for an unreachable registry; "+
			"the image's absence was never established. Got: %#v", aerr)
	}
	if aerr.Code != "ServerException" {
		t.Errorf("describeImages error code = %q, want ServerException", aerr.Code)
	}
	if aerr.HTTPStatus != http.StatusInternalServerError {
		t.Errorf("describeImages HTTP status = %d, want 500", aerr.HTTPStatus)
	}
}

// A reachable registry that genuinely does not hold the image must still say
// so. Without this the fix above could be "always ServerException", which would
// break the case the API exists to answer.
func TestDescribeImages_stillAnswersImageNotFoundWhenTheRegistryIsReachable(t *testing.T) {
	registry := &fakeRegistry{absent: true}
	s := syncService(t, registry.start(t))
	seedRepo(t, s)

	_, aerr := s.describeImagesTyped(context.Background(), &describeImagesRequest{
		RepositoryName: syncRepo,
		ImageIds:       []ImageIdentifier{{ImageTag: "really-not-there"}},
	})

	if aerr == nil {
		t.Fatal("describeImages succeeded for an image the registry does not hold")
	}
	if aerr.Code != "ImageNotFoundException" {
		t.Errorf("describeImages error code = %q, want ImageNotFoundException when the registry answered", aerr.Code)
	}
}

// DeleteRepository's guard deliberately goes the other way: an unreachable
// registry falls back to what the store knows rather than refusing every
// delete. That choice is documented on repoHoldsImages, and this pins it so the
// read-path change above cannot be extended over it by accident.
func TestRepoHoldsImages_fallsBackToTheStoreWhenTheRegistryIsUnreachable(t *testing.T) {
	s := unreachableService(t)
	seedImage(t, s, "sha256:bbb", "kept", true)

	held, err := s.repoHoldsImages(context.Background(), syncRegion, syncRepo)
	if err != nil {
		t.Fatalf("repoHoldsImages: %v", err)
	}
	if !held {
		t.Error("repoHoldsImages = false; an unreachable registry must not make a non-empty repository look deletable")
	}
}
