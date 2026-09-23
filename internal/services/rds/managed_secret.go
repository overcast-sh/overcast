package rds

// managed_secret.go — ManageMasterUserPassword, AWS's "let RDS hold the
// password for you" option on CreateDBInstance/CreateDBCluster and
// ModifyDBInstance/ModifyDBCluster.
//
// On AWS, setting it mints a Secrets Manager secret RDS itself owns (named
// "rds!db-<uuid>" or "rds!cluster-<uuid>"), generates the master password into
// it, and returns a MasterUserSecret{SecretArn, SecretStatus, KmsKeyId} block
// on every response that would otherwise echo MasterUserPassword. Overcast
// mirrors that by reaching Secrets Manager in-process — the same seam ECS
// uses to resolve a task's `secrets` (see SecretsManagerResolver in the ecs
// package) — except RDS also has to create and delete the secret, not just
// read one that already exists.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// SecretsManagerAccess is the in-process seam RDS uses to create and remove
// the Secrets Manager secret behind a managed master password. Wired by
// Service.SetSecretsManager, the same way SetVPCResolver wires EC2.
type SecretsManagerAccess interface {
	// CreateManagedSecret creates a new secret named name, storing
	// secretString as its SecretString, and returns the secret's ARN.
	CreateManagedSecret(ctx context.Context, name, description, secretString string) (arn string, aerr *protocol.AWSError)
	// DeleteManagedSecret immediately deletes the secret identified by name
	// or ARN, bypassing the normal recovery window. A secret that is already
	// gone is not an error.
	DeleteManagedSecret(ctx context.Context, secretID string) *protocol.AWSError
}

// SetSecretsManager wires the Secrets Manager seam ManageMasterUserPassword
// uses. Left unset, a request that asks RDS to manage the master password is
// refused rather than silently skipping secret creation — see
// errManagedPasswordUnavailable.
func (s *Service) SetSecretsManager(sm SecretsManagerAccess) {
	s.handler.secretsManager = sm
}

// managedSecretJSON is the SecretString of an RDS-managed master secret:
// {"username": "...", "password": "..."} and nothing else. The longer shape
// with engine, host, port and dbname is the one Secrets Manager's Lambda
// rotation templates need in a secret a user creates; RDS rotates its own
// secrets without a Lambda, so they carry no connection details. Code that
// wants the endpoint reads it from DescribeDBInstances, as it must on AWS —
// adding host/port here would let code pass locally that fails there.
type managedSecretJSON struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func marshalManagedSecretJSON(username, password string) (string, error) {
	body, err := json.Marshal(managedSecretJSON{Username: username, Password: password})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// createManagedMasterSecret mints a managed master password for a newly
// created (or newly-managed, via Modify) DB instance or cluster: it
// generates a password valid for engine, stores it in a new Secrets Manager
// secret named the way AWS names one ("rds!db-<uuid>" / "rds!cluster-<uuid>"),
// and returns both. kind is "db" or "cluster".
func (h *Handler) createManagedMasterSecret(ctx context.Context, engine, kind, resourceID, masterUsername, kmsKeyID string) (password, secretARN string, aerr *protocol.AWSError) {
	if h.secretsManager == nil {
		return "", "", errManagedPasswordUnavailable()
	}
	password, aerr = generateManagedMasterPassword(engine)
	if aerr != nil {
		return "", "", aerr
	}
	name := "rds!" + kind + "-" + uuid.New().String()
	body, err := marshalManagedSecretJSON(masterUsername, password)
	if err != nil {
		return "", "", protocol.Wrap(protocol.ErrInternalError, err)
	}
	description := fmt.Sprintf("Managed by the RDS service for the DB %s %s in this account.", kindLabel(kind), resourceID)
	arn, aerr := h.secretsManager.CreateManagedSecret(ctx, name, description, body)
	if aerr != nil {
		return "", "", aerr
	}
	return password, arn, nil
}

// kindLabel turns createManagedMasterSecret's ARN-fragment "kind" ("db" or
// "cluster") into the word its description sentence reads naturally with.
func kindLabel(kind string) string {
	if kind == "cluster" {
		return "cluster"
	}
	return "instance"
}

// deleteManagedSecretBestEffort removes the Secrets Manager secret behind a
// managed master password when the resource that owns it is deleted — the
// fate AWS gives that secret. A failure here does not fail the surrounding
// DB deletion: on AWS the database is already gone by the time this would
// run, and refusing to finish deleting the instance/cluster over a secret
// that can be cleaned up later is a worse outcome than a stray secret.
func (h *Handler) deleteManagedSecretBestEffort(ctx context.Context, secretARN string) {
	if secretARN == "" || h.secretsManager == nil {
		return
	}
	if aerr := h.secretsManager.DeleteManagedSecret(ctx, secretARN); aerr != nil {
		h.log.Warn("RDS: failed to delete managed master user secret",
			zap.String("secretARN", secretARN), zap.String("error", aerr.Message))
	}
}

// errManagedPasswordUnavailable is what a ManageMasterUserPassword request
// answers when nothing wired RDS to Secrets Manager. It is Overcast's own
// wiring failing, not a caller error — the same reasoning
// errMasterPasswordNotApplied uses for a password the engine would not take.
func errManagedPasswordUnavailable() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InternalError",
		Message:    "RDS cannot manage the master user password: Secrets Manager is not available.",
		HTTPStatus: http.StatusInternalServerError,
	}
}
