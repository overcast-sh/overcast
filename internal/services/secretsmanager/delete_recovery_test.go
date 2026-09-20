package secretsmanager

import (
	"context"
	"testing"
	"time"
)

// The recovery window is enforced lazily, by comparing the stored DeletedDate
// against the clock on every read. These tests advance a bare mock clock by
// days, which is why they live here rather than in tests/integration: the test
// server's mock clock carries every service's tickers, and advancing it a week
// makes the mock deliver a week of ticks one at a time.

func TestDeleteSecret_secretSurvivesUntilTheWindowElapses(t *testing.T) {
	// Given: a secret scheduled for deletion with a 7-day window
	h, mock := newRotationTestHandler(t)
	ctx := context.Background()
	seedSecret(t, h, "expiring", "val")
	window := int64(7)
	if _, aerr := h.deleteSecretTyped(ctx, &deleteSecretRequest{
		SecretId: "expiring", RecoveryWindowInDays: &window,
	}); aerr != nil {
		t.Fatalf("DeleteSecret: %v", aerr)
	}

	// When: the clock stops just short of the window
	mock.Add(7*24*time.Hour - time.Second)

	// Then: the secret is still there, and still restorable
	if _, aerr := h.describeSecretTyped(ctx, &secretIDRequest{SecretId: "expiring"}); aerr != nil {
		t.Fatalf("DescribeSecret before the window elapsed: %v", aerr)
	}
	if _, aerr := h.restoreSecretTyped(ctx, &secretIDRequest{SecretId: "expiring"}); aerr != nil {
		t.Fatalf("RestoreSecret before the window elapsed: %v", aerr)
	}
}

func TestDeleteSecret_secretIsGoneOnceTheWindowElapses(t *testing.T) {
	// Given: a secret scheduled for deletion with a 7-day window
	h, mock := newRotationTestHandler(t)
	ctx := context.Background()
	seedSecret(t, h, "expiring", "val")
	window := int64(7)
	out, aerr := h.deleteSecretTyped(ctx, &deleteSecretRequest{
		SecretId: "expiring", RecoveryWindowInDays: &window,
	})
	if aerr != nil {
		t.Fatalf("DeleteSecret: %v", aerr)
	}
	if want := float64(mock.Now().Add(7 * 24 * time.Hour).Unix()); out.DeletionDate != want {
		t.Fatalf("DeletionDate = %v, want %v", out.DeletionDate, want)
	}

	// When: the window runs out
	mock.Add(7 * 24 * time.Hour)

	// Then: every lookup reports the secret as gone
	if _, aerr := h.describeSecretTyped(ctx, &secretIDRequest{SecretId: "expiring"}); aerr == nil {
		t.Fatal("DescribeSecret found a secret whose recovery window had elapsed")
	} else if aerr.Code != "ResourceNotFoundException" {
		t.Errorf("DescribeSecret error = %q, want ResourceNotFoundException", aerr.Code)
	}
	if _, aerr := h.restoreSecretTyped(ctx, &secretIDRequest{SecretId: "expiring"}); aerr == nil {
		t.Fatal("RestoreSecret revived a secret whose recovery window had elapsed")
	} else if aerr.Code != "ResourceNotFoundException" {
		t.Errorf("RestoreSecret error = %q, want ResourceNotFoundException", aerr.Code)
	}

	// And: the name is free again
	if _, aerr := h.createSecretTyped(ctx, &createSecretRequest{Name: "expiring", SecretString: "again"}); aerr != nil {
		t.Fatalf("CreateSecret after the window elapsed: %v", aerr)
	}
	if got := currentValue(t, h, "expiring"); got != "again" {
		t.Errorf("recreated secret value = %q, want again", got)
	}
}

func TestListSecrets_hidesASecretWhoseWindowElapsed(t *testing.T) {
	// Given: one live secret and one whose recovery window has run out
	h, mock := newRotationTestHandler(t)
	ctx := context.Background()
	seedSecret(t, h, "live", "val")
	seedSecret(t, h, "expired", "val")
	if _, aerr := h.deleteSecretTyped(ctx, &deleteSecretRequest{SecretId: "expired"}); aerr != nil {
		t.Fatalf("DeleteSecret: %v", aerr)
	}
	mock.Add(30 * 24 * time.Hour)

	// When: ListSecrets asks for planned deletions too
	out, aerr := h.listSecretsTyped(ctx, &listSecretsRequest{IncludePlannedDeletion: true})
	if aerr != nil {
		t.Fatalf("ListSecrets: %v", aerr)
	}

	// Then: the expired secret is not listed even so — the window has passed,
	// so it is deleted rather than planned for deletion
	for _, entry := range out.SecretList {
		if entry.Name == "expired" {
			t.Fatalf("ListSecrets listed a secret whose window elapsed: %+v", out.SecretList)
		}
	}
	if len(out.SecretList) != 1 || out.SecretList[0].Name != "live" {
		t.Errorf("expected only the live secret, got %+v", out.SecretList)
	}
}

func TestServiceSecretValue_refusesASecretMarkedForDeletion(t *testing.T) {
	// Given: a secret scheduled for deletion
	h, _ := newRotationTestHandler(t)
	ctx := context.Background()
	seedSecret(t, h, "ecs-secret", "val")
	service := &Service{handler: h}
	if got, ok := service.SecretValue(ctx, "ecs-secret"); !ok || got != "val" {
		t.Fatalf("SecretValue before delete = %q, %v; want val, true", got, ok)
	}
	if _, aerr := h.deleteSecretTyped(ctx, &deleteSecretRequest{SecretId: "ecs-secret"}); aerr != nil {
		t.Fatalf("DeleteSecret: %v", aerr)
	}

	// When: a consumer resolves it — an ECS container definition's `secrets`
	// Then: the value is withheld, as GetSecretValue withholds it
	if got, ok := service.SecretValue(ctx, "ecs-secret"); ok {
		t.Errorf("SecretValue returned %q for a secret marked for deletion", got)
	}
}
