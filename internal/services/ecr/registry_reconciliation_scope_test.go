package ecr

// registry_reconciliation_scope_test.go — how far the registry-reconciliation
// sweep's outcome reaches, and where it deliberately does not.
//
// syncRepoImagesFromRegistry is shared by ListImages, DescribeImages and
// BatchGetImage, but only two of the three act on sweepUnavailable.
// listImagesTyped says why in its own comment: an empty or stale list is
// preferred over refusing a listing that names no specific image to be wrong
// about. DescribeImages and BatchGetImage differ because they can name the
// image they failed to find, and a ServerException is the honest answer only
// when "not found" was never established. These tests pin both halves of that
// split — ListImages staying silent is intentional, not untested drift — and
// the corresponding gap in BatchDeleteImage, which never asks the registry to
// delete anything it deletes a record for.

import (
	"context"
	"testing"
)

// TestListImages_silentlyServesTheStoreWhenTheRegistryIsUnreachable pins the
// deliberate asymmetry documented on listImagesTyped: unlike DescribeImages
// and BatchGetImage, a transiently unreachable registry does not turn into a
// ServerException here. The caller gets whatever the store currently holds,
// with no signal that the sweep could not confirm it.
func TestListImages_silentlyServesTheStoreWhenTheRegistryIsUnreachable(t *testing.T) {
	s := unreachableService(t)
	seedRepo(t, s)
	seedImage(t, s, "sha256:aaa", "live", true)

	resp, aerr := s.listImagesTyped(context.Background(), &repoRefRequest{RepositoryName: syncRepo})
	if aerr != nil {
		t.Fatalf("ListImages against an unreachable registry returned an error: %v — want it to serve the store silently", aerr)
	}
	if len(resp.ImageIds) != 1 || resp.ImageIds[0].ImageTag != "live" {
		t.Fatalf("ListImages = %#v, want the one seeded record", resp.ImageIds)
	}
}

// TestBatchGetImage_answersServerExceptionWhenTheRegistryIsUnreachable mirrors
// TestDescribeImages_answersServerExceptionWhenTheRegistryIsUnreachable
// (registry_unavailable_test.go): batchGetImageTyped carries the identical
// sweep-state check, but nothing had pinned it before this.
func TestBatchGetImage_answersServerExceptionWhenTheRegistryIsUnreachable(t *testing.T) {
	s := unreachableService(t)
	seedRepo(t, s)

	_, aerr := s.batchGetImageTyped(context.Background(), &imageIDSetRequest{
		RepositoryName: syncRepo,
		ImageIds:       []ImageIdentifier{{ImageTag: "never-swept"}},
	})

	if aerr == nil {
		t.Fatal("batchGetImage succeeded against an unreachable registry")
	}
	if aerr.Code == "ImageNotFoundException" {
		t.Errorf("batchGetImage reported ImageNotFoundException for an unreachable registry; the image's absence was never established. Got: %#v", aerr)
	}
	if aerr.Code != "ServerException" {
		t.Errorf("batchGetImage error code = %q, want ServerException", aerr.Code)
	}
}

// TestBatchGetImage_stillAnswersPerImageFailuresWhenTheRegistryIsReachable is
// the other half: a reachable registry that genuinely lacks the image must
// still report it as a per-image failure rather than failing the whole call.
func TestBatchGetImage_stillAnswersPerImageFailuresWhenTheRegistryIsReachable(t *testing.T) {
	registry := &fakeRegistry{absent: true}
	s := syncService(t, registry.start(t))
	seedRepo(t, s)

	resp, aerr := s.batchGetImageTyped(context.Background(), &imageIDSetRequest{
		RepositoryName: syncRepo,
		ImageIds:       []ImageIdentifier{{ImageTag: "really-not-there"}},
	})
	if aerr != nil {
		t.Fatalf("batchGetImage: %v", aerr)
	}
	if len(resp.Failures) != 1 || resp.Failures[0].FailureCode != "ImageNotFoundException" {
		t.Fatalf("batchGetImage failures = %#v, want one ImageNotFoundException entry", resp.Failures)
	}
}

// TestBatchDeleteImage_deletedImageReappearsWhenTheRegistryStillServesIt pins
// the gap BatchDeleteImage's capability Notes now name: the operation only
// ever deletes the store record. It never asks the registry to delete the
// manifest, so when Docker backs the repository and the registry still serves
// the bytes, the very next reconciling read (ListImages, DescribeImages,
// BatchGetImage) re-discovers the "deleted" image and writes its record right
// back — unlike real ECR, where BatchDeleteImage's effect is durable.
func TestBatchDeleteImage_deletedImageReappearsWhenTheRegistryStillServesIt(t *testing.T) {
	reg := &fakeRegistry{tags: map[string]string{"still-there": "sha256:aaa"}}
	s := syncService(t, reg.start(t))
	seedRepo(t, s)
	seedImage(t, s, "sha256:aaa", "still-there", true)

	// When: the image is deleted through the API everyone calls to make a
	// deletion stick.
	deleted, aerr := s.batchDeleteImageTyped(context.Background(), &imageIDSetRequest{
		RepositoryName: syncRepo,
		ImageIds:       []ImageIdentifier{{ImageTag: "still-there"}},
	})
	if aerr != nil {
		t.Fatalf("batchDeleteImage: %v", aerr)
	}
	if len(deleted.ImageIds) != 1 {
		t.Fatalf("batchDeleteImage deleted = %#v, want the one requested image", deleted.ImageIds)
	}
	if got := storedTags(t, s); len(got) != 0 {
		t.Fatalf("stored images after delete = %v, want none", got)
	}

	// Then: the very next read that sweeps the registry brings it straight
	// back, because BatchDeleteImage never told the registry to forget it.
	resp, aerr := s.listImagesTyped(context.Background(), &repoRefRequest{RepositoryName: syncRepo})
	if aerr != nil {
		t.Fatalf("ListImages after delete: %v", aerr)
	}
	if len(resp.ImageIds) != 1 || resp.ImageIds[0].ImageTag != "still-there" {
		t.Fatalf("ListImages after delete = %#v, want the registry-reconciled image back — "+
			"this is the documented gap, not a passing assertion; if it starts failing, "+
			"BatchDeleteImage now reaches the registry and the capability Notes need updating", resp.ImageIds)
	}
}
