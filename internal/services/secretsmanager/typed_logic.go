package secretsmanager

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// smTagCfg tunes the shared tag validator to Secrets Manager's error shape
// (#1052). Every other rejected-input case this package models already
// answers InvalidParameterException, and it declares no dedicated tag-count
// exception, so the 50-tag limit is reported the same way.
var smTagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    "InvalidParameterException",
	InvalidCode:     "InvalidParameterException",
	ExceededMessage: "You can create a maximum of 50 tags for a secret.",
}

func smTagsToMap(tags []Tag) map[string]string {
	m := make(map[string]string, len(tags))
	for _, t := range tags {
		m[t.Key] = t.Value
	}
	return m
}

type secretIDRequest struct {
	SecretId string `json:"SecretId" cbor:"SecretId"`
}

type createSecretRequest struct {
	Name         string `json:"Name" cbor:"Name"`
	SecretString string `json:"SecretString" cbor:"SecretString"`
	SecretBinary string `json:"SecretBinary" cbor:"SecretBinary"`
	Description  string `json:"Description" cbor:"Description"`
	KmsKeyId     string `json:"KmsKeyId" cbor:"KmsKeyId"`
	Tags         []Tag  `json:"Tags" cbor:"Tags"`
}

type createSecretResponse struct {
	ARN       string `json:"ARN" cbor:"ARN"`
	Name      string `json:"Name" cbor:"Name"`
	VersionId string `json:"VersionId" cbor:"VersionId"`
}

type getSecretValueRequest struct {
	SecretId     string `json:"SecretId" cbor:"SecretId"`
	VersionId    string `json:"VersionId" cbor:"VersionId"`
	VersionStage string `json:"VersionStage" cbor:"VersionStage"`
}

type secretValueResponse struct {
	ARN           string   `json:"ARN" cbor:"ARN"`
	Name          string   `json:"Name" cbor:"Name"`
	VersionId     string   `json:"VersionId" cbor:"VersionId"`
	VersionStages []string `json:"VersionStages" cbor:"VersionStages"`
	SecretString  string   `json:"SecretString,omitempty" cbor:"SecretString,omitempty"`
	SecretBinary  string   `json:"SecretBinary,omitempty" cbor:"SecretBinary,omitempty"`
	CreatedDate   float64  `json:"CreatedDate" cbor:"CreatedDate"`
}

type describeSecretResponse struct {
	ARN                string              `json:"ARN" cbor:"ARN"`
	Name               string              `json:"Name" cbor:"Name"`
	Description        string              `json:"Description" cbor:"Description"`
	KmsKeyId           string              `json:"KmsKeyId,omitempty" cbor:"KmsKeyId,omitempty"`
	CreatedDate        float64             `json:"CreatedDate" cbor:"CreatedDate"`
	LastChangedDate    float64             `json:"LastChangedDate" cbor:"LastChangedDate"`
	Tags               []Tag               `json:"Tags" cbor:"Tags"`
	VersionIdsToStages map[string][]string `json:"VersionIdsToStages" cbor:"VersionIdsToStages"`
	RotationEnabled    bool                `json:"RotationEnabled" cbor:"RotationEnabled"`
	RotationRules      *RotationRules      `json:"RotationRules,omitempty" cbor:"RotationRules,omitempty"`
	RotationLambdaARN  string              `json:"RotationLambdaARN,omitempty" cbor:"RotationLambdaARN,omitempty"`
	LastRotatedDate    float64             `json:"LastRotatedDate,omitempty" cbor:"LastRotatedDate,omitempty"`
	NextRotationDate   float64             `json:"NextRotationDate,omitempty" cbor:"NextRotationDate,omitempty"`
	DeletedDate        float64             `json:"DeletedDate,omitempty" cbor:"DeletedDate,omitempty"`
}

type putSecretValueRequest struct {
	SecretId           string   `json:"SecretId" cbor:"SecretId"`
	SecretString       string   `json:"SecretString" cbor:"SecretString"`
	SecretBinary       string   `json:"SecretBinary" cbor:"SecretBinary"`
	ClientRequestToken string   `json:"ClientRequestToken" cbor:"ClientRequestToken"`
	VersionStages      []string `json:"VersionStages" cbor:"VersionStages"`
}

type putSecretValueResponse struct {
	ARN           string   `json:"ARN" cbor:"ARN"`
	Name          string   `json:"Name" cbor:"Name"`
	VersionId     string   `json:"VersionId" cbor:"VersionId"`
	VersionStages []string `json:"VersionStages" cbor:"VersionStages"`
}

type updateSecretRequest struct {
	SecretId     string  `json:"SecretId" cbor:"SecretId"`
	Description  *string `json:"Description" cbor:"Description"`
	SecretString string  `json:"SecretString" cbor:"SecretString"`
	SecretBinary string  `json:"SecretBinary" cbor:"SecretBinary"`
	KmsKeyId     *string `json:"KmsKeyId" cbor:"KmsKeyId"`
}

type updateSecretResponse struct {
	ARN       string `json:"ARN" cbor:"ARN"`
	Name      string `json:"Name" cbor:"Name"`
	VersionId string `json:"VersionId,omitempty" cbor:"VersionId,omitempty"`
}

type listSecretsRequest struct {
	Filters []secretFilter `json:"Filters" cbor:"Filters"`
	// IncludePlannedDeletion asks for secrets inside their recovery window,
	// which ListSecrets otherwise leaves out.
	IncludePlannedDeletion bool `json:"IncludePlannedDeletion" cbor:"IncludePlannedDeletion"`
}

type secretListEntry struct {
	ARN               string         `json:"ARN" cbor:"ARN"`
	Name              string         `json:"Name" cbor:"Name"`
	Description       string         `json:"Description" cbor:"Description"`
	KmsKeyId          string         `json:"KmsKeyId,omitempty" cbor:"KmsKeyId,omitempty"`
	CreatedDate       float64        `json:"CreatedDate" cbor:"CreatedDate"`
	LastChangedDate   float64        `json:"LastChangedDate" cbor:"LastChangedDate"`
	Tags              []Tag          `json:"Tags,omitempty" cbor:"Tags,omitempty"`
	RotationEnabled   bool           `json:"RotationEnabled" cbor:"RotationEnabled"`
	RotationRules     *RotationRules `json:"RotationRules,omitempty" cbor:"RotationRules,omitempty"`
	RotationLambdaARN string         `json:"RotationLambdaARN,omitempty" cbor:"RotationLambdaARN,omitempty"`
	LastRotatedDate   float64        `json:"LastRotatedDate,omitempty" cbor:"LastRotatedDate,omitempty"`
	NextRotationDate  float64        `json:"NextRotationDate,omitempty" cbor:"NextRotationDate,omitempty"`
	DeletedDate       float64        `json:"DeletedDate,omitempty" cbor:"DeletedDate,omitempty"`
}

type listSecretsResponse struct {
	SecretList []secretListEntry `json:"SecretList" cbor:"SecretList"`
}

type secretVersionEntry struct {
	VersionId     string   `json:"VersionId" cbor:"VersionId"`
	VersionStages []string `json:"VersionStages" cbor:"VersionStages"`
	CreatedDate   float64  `json:"CreatedDate" cbor:"CreatedDate"`
}

type listSecretVersionIdsResponse struct {
	ARN      string               `json:"ARN" cbor:"ARN"`
	Name     string               `json:"Name" cbor:"Name"`
	Versions []secretVersionEntry `json:"Versions" cbor:"Versions"`
}

// deleteSecretRequest takes both optional parameters as pointers because AWS
// rejects a call that *specifies* both, whatever their values. The model gives
// each `smithy.api#default: null`, so every SDK omits the one the caller left
// alone, and an explicit ForceDeleteWithoutRecovery=false is a different
// request from an absent one.
type deleteSecretRequest struct {
	SecretId                   string `json:"SecretId" cbor:"SecretId"`
	ForceDeleteWithoutRecovery *bool  `json:"ForceDeleteWithoutRecovery" cbor:"ForceDeleteWithoutRecovery"`
	RecoveryWindowInDays       *int64 `json:"RecoveryWindowInDays" cbor:"RecoveryWindowInDays"`
}

type deleteSecretResponse struct {
	ARN          string  `json:"ARN" cbor:"ARN"`
	Name         string  `json:"Name" cbor:"Name"`
	DeletionDate float64 `json:"DeletionDate" cbor:"DeletionDate"`
}

type restoreSecretResponse struct {
	ARN  string `json:"ARN" cbor:"ARN"`
	Name string `json:"Name" cbor:"Name"`
}

type tagResourceRequest struct {
	SecretId string `json:"SecretId" cbor:"SecretId"`
	Tags     []Tag  `json:"Tags" cbor:"Tags"`
}

type rotateSecretRequest struct {
	SecretId           string         `json:"SecretId" cbor:"SecretId"`
	RotationLambdaARN  string         `json:"RotationLambdaARN" cbor:"RotationLambdaARN"`
	RotationRules      *RotationRules `json:"RotationRules" cbor:"RotationRules"`
	RotateImmediately  *bool          `json:"RotateImmediately" cbor:"RotateImmediately"`
	ClientRequestToken string         `json:"ClientRequestToken" cbor:"ClientRequestToken"`
}

type rotateSecretResponse struct {
	ARN       string `json:"ARN" cbor:"ARN"`
	Name      string `json:"Name" cbor:"Name"`
	VersionId string `json:"VersionId" cbor:"VersionId"`
}

type cancelRotateSecretResponse struct {
	ARN       string `json:"ARN" cbor:"ARN"`
	Name      string `json:"Name" cbor:"Name"`
	VersionId string `json:"VersionId,omitempty" cbor:"VersionId,omitempty"`
}

type updateSecretVersionStageRequest struct {
	SecretId            string `json:"SecretId" cbor:"SecretId"`
	VersionStage        string `json:"VersionStage" cbor:"VersionStage"`
	RemoveFromVersionId string `json:"RemoveFromVersionId" cbor:"RemoveFromVersionId"`
	MoveToVersionId     string `json:"MoveToVersionId" cbor:"MoveToVersionId"`
}

type updateSecretVersionStageResponse struct {
	ARN  string `json:"ARN" cbor:"ARN"`
	Name string `json:"Name" cbor:"Name"`
}

type untagResourceRequest struct {
	SecretId string   `json:"SecretId" cbor:"SecretId"`
	TagKeys  []string `json:"TagKeys" cbor:"TagKeys"`
}

type getRandomPasswordRequest struct {
	PasswordLength     *int64 `json:"PasswordLength" cbor:"PasswordLength"`
	ExcludeCharacters  string `json:"ExcludeCharacters" cbor:"ExcludeCharacters"`
	ExcludeNumbers     bool   `json:"ExcludeNumbers" cbor:"ExcludeNumbers"`
	ExcludePunctuation bool   `json:"ExcludePunctuation" cbor:"ExcludePunctuation"`
	ExcludeUppercase   bool   `json:"ExcludeUppercase" cbor:"ExcludeUppercase"`
	ExcludeLowercase   bool   `json:"ExcludeLowercase" cbor:"ExcludeLowercase"`
	IncludeSpace       bool   `json:"IncludeSpace" cbor:"IncludeSpace"`
	// RequireEachIncludedType is a pointer because AWS defaults it to true, so
	// absent and false are different requests.
	RequireEachIncludedType *bool `json:"RequireEachIncludedType" cbor:"RequireEachIncludedType"`
}

type getRandomPasswordResponse struct {
	RandomPassword string `json:"RandomPassword" cbor:"RandomPassword"`
}

type batchGetSecretValueRequest struct {
	SecretIdList []string       `json:"SecretIdList" cbor:"SecretIdList"`
	Filters      []secretFilter `json:"Filters" cbor:"Filters"`
}

type batchGetSecretValueResponse struct {
	SecretValues []secretValueResponse `json:"SecretValues" cbor:"SecretValues"`
	Errors       []batchSecretError    `json:"Errors" cbor:"Errors"`
}

type batchSecretError struct {
	SecretId  string `json:"SecretId" cbor:"SecretId"`
	ErrorCode string `json:"ErrorCode" cbor:"ErrorCode"`
	Message   string `json:"Message" cbor:"Message"`
}

func (h *Handler) publishCtx(ctx context.Context, t events.Type, payload any) {
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: t, Payload: payload})
	}
}

// resolveLiveSecret resolves a secret that must not be inside a recovery
// window. Every operation on a secret's value goes through it: AWS answers
// InvalidRequestException for those while the secret is marked for deletion,
// and keeps DescribeSecret, RestoreSecret and a forced DeleteSecret working,
// which is why those three call resolveSecret directly.
func (h *Handler) resolveLiveSecret(ctx context.Context, secretID string) (*Secret, *protocol.AWSError) {
	sec, aerr := h.store.resolveSecret(ctx, secretID)
	if aerr != nil {
		return nil, aerr
	}
	if sec.markedForDeletion(h.store.now()) {
		return nil, errMarkedForDeletion()
	}
	return sec, nil
}

func (h *Handler) createSecretTyped(ctx context.Context, req *createSecretRequest) (*createSecretResponse, *protocol.AWSError) {
	log := h.log.WithRecorder(ctx)
	if req.Name == "" {
		return nil, errInvalidParameter("You must provide a value for the Name parameter.")
	}
	if utf8.RuneCountInString(req.KmsKeyId) > 2048 {
		return nil, errInvalidParameter("KmsKeyId exceeds the maximum length of 2048 characters.")
	}
	if existing, aerr := h.store.getSecret(ctx, req.Name); aerr == nil {
		// A secret inside its recovery window still holds the name: AWS refuses
		// the create and tells the caller to restore it or force-delete it.
		if existing.markedForDeletion(h.store.now()) {
			return nil, errInvalidRequest(
				"You can't create this secret because a secret with this name is already scheduled for deletion.")
		}
		return nil, errResourceExists(req.Name)
	}
	// Request-shape validation before the secret is created — the same
	// ordering createLogGroupTyped uses (internal/services/cloudwatch/logs/
	// typed_logic.go) — so a rejected create leaves no secret behind with
	// nothing able to fix its tags (#1052).
	if aerr := serviceutil.ValidateTags(smTagCfg, smTagsToMap(req.Tags)); aerr != nil {
		return nil, aerr
	}

	now := h.store.now()
	versionId := uuid.New().String()
	arn := secretARN(h.store.region(ctx), h.cfg.AccountID, req.Name)

	sec := &Secret{
		ARN:         arn,
		Name:        req.Name,
		Description: req.Description,
		KmsKeyId:    req.KmsKeyId,
		Tags:        req.Tags,
		Versions: []SecretVersion{{
			VersionId:    versionId,
			SecretString: req.SecretString,
			SecretBinary: req.SecretBinary,
			Stages:       []string{stageAWSCurrent},
			CreatedDate:  float64(now.Unix()),
		}},
		CurrentVersionId: versionId,
		CreatedDate:      float64(now.Unix()),
		LastChangedDate:  float64(now.Unix()),
	}
	if aerr := h.store.putSecret(ctx, sec); aerr != nil {
		return nil, aerr
	}

	h.publishCtx(ctx, events.SecretCreated, events.ResourcePayload{Name: req.Name, ARN: arn})
	log.Info("secret created", zap.String("name", req.Name))
	return &createSecretResponse{ARN: arn, Name: req.Name, VersionId: versionId}, nil
}

func (h *Handler) getSecretValueTyped(ctx context.Context, req *getSecretValueRequest) (*secretValueResponse, *protocol.AWSError) {
	sec, aerr := h.resolveLiveSecret(ctx, req.SecretId)
	if aerr != nil {
		return nil, aerr
	}
	version, aerr := selectSecretVersion(sec, req.VersionId, req.VersionStage)
	if aerr != nil {
		return nil, aerr
	}
	if version.SecretString == "" && version.SecretBinary == "" {
		// A version staged for a rotation whose createSecret step has not put a
		// value in it yet. AWS reports it as not found rather than returning an
		// empty 200, and the rotation blueprints branch on precisely that: the
		// ResourceNotFoundException from reading their own AWSPENDING version is
		// how they tell "I still have a password to generate" from "I already
		// generated one and this is a retry". DescribeSecret still lists it —
		// the version exists, it just holds nothing.
		return nil, errNoSecretValue(version.VersionId, req.VersionStage)
	}
	return secretValueOut(sec, version), nil
}

// selectSecretVersion resolves the VersionId/VersionStage pair the way AWS
// does: either identifies a version, both must agree, and neither defaults to
// "whatever is newest" — the default is the AWSCURRENT staging label.
func selectSecretVersion(sec *Secret, versionID, stage string) (*SecretVersion, *protocol.AWSError) {
	if versionID == "" && stage == "" {
		stage = stageAWSCurrent
	}

	var byID, byStage *SecretVersion
	if versionID != "" {
		if byID = sec.versionByID(versionID); byID == nil {
			return nil, errResourceNotFound(sec.Name)
		}
	}
	if stage != "" {
		if byStage = sec.versionByStage(stage); byStage == nil {
			return nil, &protocol.AWSError{
				Code:       "ResourceNotFoundException",
				Message:    "Secrets Manager can't find the specified secret value for staging label: " + stage,
				HTTPStatus: 400,
			}
		}
	}
	switch {
	case byID != nil && byStage != nil && byID.VersionId != byStage.VersionId:
		return nil, errInvalidParameter("The VersionId and VersionStage parameters refer to different versions of the secret.")
	case byID != nil:
		return byID, nil
	default:
		return byStage, nil
	}
}

func (h *Handler) describeSecretTyped(ctx context.Context, req *secretIDRequest) (*describeSecretResponse, *protocol.AWSError) {
	sec, aerr := h.store.resolveSecret(ctx, req.SecretId)
	if aerr != nil {
		return nil, aerr
	}

	versionMap := make(map[string][]string, len(sec.Versions))
	for _, v := range sec.Versions {
		versionMap[v.VersionId] = v.Stages
	}
	return &describeSecretResponse{
		ARN:                sec.ARN,
		Name:               sec.Name,
		Description:        sec.Description,
		KmsKeyId:           sec.KmsKeyId,
		CreatedDate:        sec.CreatedDate,
		LastChangedDate:    sec.LastChangedDate,
		Tags:               sec.Tags,
		VersionIdsToStages: versionMap,
		RotationEnabled:    sec.RotationEnabled,
		RotationRules:      sec.RotationRules,
		RotationLambdaARN:  sec.RotationLambdaARN,
		LastRotatedDate:    sec.LastRotatedDate,
		NextRotationDate:   sec.NextRotationDate,
		DeletedDate:        sec.DeletedDate,
	}, nil
}

func (h *Handler) putSecretValueTyped(ctx context.Context, req *putSecretValueRequest) (*putSecretValueResponse, *protocol.AWSError) {
	sec, aerr := h.resolveLiveSecret(ctx, req.SecretId)
	if aerr != nil {
		return nil, aerr
	}
	version, aerr := h.stageVersion(sec, req.ClientRequestToken, req.SecretString, req.SecretBinary, req.VersionStages)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := h.store.putSecret(ctx, sec); aerr != nil {
		return nil, aerr
	}
	h.publishCtx(ctx, events.SecretUpdated, events.ResourcePayload{Name: sec.Name, ARN: sec.ARN})
	return &putSecretValueResponse{
		ARN:           sec.ARN,
		Name:          sec.Name,
		VersionId:     version.VersionId,
		VersionStages: version.Stages,
	}, nil
}

// stageVersion writes a version under the requested staging labels, which is
// the whole of what a rotation function's createSecret step does.
//
// AWS's contract, and why each part matters here:
//   - ClientRequestToken *is* the version ID. The rotation protocol relies on
//     that: every step is handed the same token and looks the version up by it.
//   - Re-putting the same token with the same value is idempotent; re-putting
//     it with a different value is ResourceExistsException.
//   - VersionStages defaults to AWSCURRENT, and taking AWSCURRENT off a version
//     moves AWSPREVIOUS onto it.
func (h *Handler) stageVersion(sec *Secret, token, secretString, secretBinary string, stages []string) (*SecretVersion, *protocol.AWSError) {
	if token == "" {
		token = uuid.New().String()
	}
	if len(stages) == 0 {
		stages = []string{stageAWSCurrent}
	}

	now := h.store.now()
	if existing := sec.versionByID(token); existing != nil {
		switch {
		case existing.SecretString == "" && existing.SecretBinary == "":
			// The version Secrets Manager staged AWSPENDING before it called
			// the rotation function. Putting the generated value into it is
			// what createSecret is *for*, so this is not the same-token
			// collision below — the version was created empty precisely so the
			// function could fill it under the token every step is handed.
			existing.SecretString = secretString
			existing.SecretBinary = secretBinary
		case existing.SecretString != secretString || existing.SecretBinary != secretBinary:
			return nil, &protocol.AWSError{
				Code:       "ResourceExistsException",
				Message:    "A version with the ClientRequestToken " + token + " already exists with different content.",
				HTTPStatus: 400,
			}
		}
	} else {
		sec.Versions = append(sec.Versions, SecretVersion{
			VersionId:    token,
			SecretString: secretString,
			SecretBinary: secretBinary,
			Stages:       []string{},
			CreatedDate:  float64(now.Unix()),
		})
	}

	for _, stage := range stages {
		displaced := sec.detachStage(stage)
		if stage == stageAWSCurrent && displaced != "" && displaced != token {
			sec.attachStage(stageAWSPrevious, displaced)
		}
		if v := sec.versionByID(token); v != nil && !containsString(v.Stages, stage) {
			v.Stages = append(v.Stages, stage)
		}
	}

	sec.syncCurrentVersionID()
	sec.pruneVersions()
	sec.LastChangedDate = float64(now.Unix())
	return sec.versionByID(token), nil
}

func (h *Handler) updateSecretVersionStageTyped(ctx context.Context, req *updateSecretVersionStageRequest) (*updateSecretVersionStageResponse, *protocol.AWSError) {
	if req.VersionStage == "" {
		return nil, errInvalidParameter("You must provide a value for the VersionStage parameter.")
	}
	if req.MoveToVersionId == "" && req.RemoveFromVersionId == "" {
		return nil, errInvalidParameter("You must specify either MoveToVersionId, RemoveFromVersionId, or both.")
	}

	sec, aerr := h.resolveLiveSecret(ctx, req.SecretId)
	if aerr != nil {
		return nil, aerr
	}
	if req.MoveToVersionId != "" && sec.versionByID(req.MoveToVersionId) == nil {
		return nil, errInvalidParameter("The secret has no version with the ID " + req.MoveToVersionId + ".")
	}
	if req.RemoveFromVersionId != "" {
		from := sec.versionByID(req.RemoveFromVersionId)
		if from == nil || !containsString(from.Stages, req.VersionStage) {
			return nil, errInvalidParameter(
				"The staging label " + req.VersionStage + " is not attached to version " + req.RemoveFromVersionId + ".")
		}
	}

	holder := sec.detachStage(req.VersionStage)
	if req.MoveToVersionId != "" {
		sec.attachStage(req.VersionStage, req.MoveToVersionId)
		// AWS moves AWSPREVIOUS onto whatever AWSCURRENT just left.
		if req.VersionStage == stageAWSCurrent && holder != "" && holder != req.MoveToVersionId {
			sec.attachStage(stageAWSPrevious, holder)
		}
	}

	sec.syncCurrentVersionID()
	sec.pruneVersions()
	sec.LastChangedDate = float64(h.store.now().Unix())
	if aerr := h.store.putSecret(ctx, sec); aerr != nil {
		return nil, aerr
	}
	return &updateSecretVersionStageResponse{ARN: sec.ARN, Name: sec.Name}, nil
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func (h *Handler) updateSecretTyped(ctx context.Context, req *updateSecretRequest) (*updateSecretResponse, *protocol.AWSError) {
	sec, aerr := h.resolveLiveSecret(ctx, req.SecretId)
	if aerr != nil {
		return nil, aerr
	}
	if req.Description != nil {
		sec.Description = *req.Description
	}
	if req.KmsKeyId != nil {
		if utf8.RuneCountInString(*req.KmsKeyId) > 2048 {
			return nil, errInvalidParameter("KmsKeyId exceeds the maximum length of 2048 characters.")
		}
		sec.KmsKeyId = *req.KmsKeyId
	}
	sec.LastChangedDate = float64(h.store.now().Unix())

	versionId := ""
	if req.SecretString != "" || req.SecretBinary != "" {
		version, aerr := h.stageVersion(sec, "", req.SecretString, req.SecretBinary, nil)
		if aerr != nil {
			return nil, aerr
		}
		versionId = version.VersionId
	}
	if aerr := h.store.putSecret(ctx, sec); aerr != nil {
		return nil, aerr
	}
	h.publishCtx(ctx, events.SecretUpdated, events.ResourcePayload{Name: sec.Name, ARN: sec.ARN})
	return &updateSecretResponse{ARN: sec.ARN, Name: sec.Name, VersionId: versionId}, nil
}

func (h *Handler) listSecretsTyped(ctx context.Context, req *listSecretsRequest) (*listSecretsResponse, *protocol.AWSError) {
	if aerr := validateSecretFilters(req.Filters); aerr != nil {
		return nil, aerr
	}
	secrets, aerr := h.store.listSecrets(ctx)
	if aerr != nil {
		return nil, aerr
	}
	now := h.store.now()
	out := make([]secretListEntry, 0, len(secrets))
	for _, sec := range secrets {
		if len(req.Filters) > 0 && !secretMatchesFilters(sec, req.Filters) {
			continue
		}
		if sec.markedForDeletion(now) && !req.IncludePlannedDeletion {
			continue
		}
		out = append(out, secretListEntry{
			ARN:               sec.ARN,
			Name:              sec.Name,
			Description:       sec.Description,
			KmsKeyId:          sec.KmsKeyId,
			CreatedDate:       sec.CreatedDate,
			LastChangedDate:   sec.LastChangedDate,
			Tags:              sec.Tags,
			RotationEnabled:   sec.RotationEnabled,
			RotationRules:     sec.RotationRules,
			RotationLambdaARN: sec.RotationLambdaARN,
			LastRotatedDate:   sec.LastRotatedDate,
			NextRotationDate:  sec.NextRotationDate,
			DeletedDate:       sec.DeletedDate,
		})
	}
	return &listSecretsResponse{SecretList: out}, nil
}

func (h *Handler) listSecretVersionIdsTyped(ctx context.Context, req *secretIDRequest) (*listSecretVersionIdsResponse, *protocol.AWSError) {
	sec, aerr := h.store.resolveSecret(ctx, req.SecretId)
	if aerr != nil {
		return nil, aerr
	}
	versions := make([]secretVersionEntry, 0, len(sec.Versions))
	for _, v := range sec.Versions {
		versions = append(versions, secretVersionEntry{
			VersionId:     v.VersionId,
			VersionStages: v.Stages,
			CreatedDate:   v.CreatedDate,
		})
	}
	return &listSecretVersionIdsResponse{ARN: sec.ARN, Name: sec.Name, Versions: versions}, nil
}

// deleteSecretTyped schedules a deletion rather than performing one, which is
// what AWS does: the secret keeps its record with a DeletedDate marking the end
// of the recovery window, and RestoreSecret can cancel it until then. Nothing
// sweeps the store when the window closes — the window is enforced on read (see
// smStore.getSecret) — so no background work outlives the request.
//
// ForceDeleteWithoutRecovery is the opt-out, and the only path that removes the
// record here and now.
func (h *Handler) deleteSecretTyped(ctx context.Context, req *deleteSecretRequest) (*deleteSecretResponse, *protocol.AWSError) {
	log := h.log.WithRecorder(ctx)
	force := req.ForceDeleteWithoutRecovery != nil && *req.ForceDeleteWithoutRecovery
	if req.ForceDeleteWithoutRecovery != nil && req.RecoveryWindowInDays != nil {
		return nil, errInvalidParameter(
			"You can't use ForceDeleteWithoutRecovery in conjunction with RecoveryWindowInDays.")
	}
	window := int64(defaultRecoveryWindowDays)
	if req.RecoveryWindowInDays != nil {
		window = *req.RecoveryWindowInDays
		if window < minRecoveryWindowDays || window > maxRecoveryWindowDays {
			return nil, errInvalidParameter(fmt.Sprintf(
				"The RecoveryWindowInDays value must be between %d and %d days, inclusive.",
				minRecoveryWindowDays, maxRecoveryWindowDays))
		}
	}

	sec, aerr := h.store.resolveSecret(ctx, req.SecretId)
	if aerr != nil {
		return nil, aerr
	}
	now := h.store.now()
	if !force && sec.markedForDeletion(now) {
		// A second scheduled delete would silently move the deadline. AWS
		// refuses it; a forced one still goes through, and is how a caller gets
		// the name back before the window closes.
		return nil, errMarkedForDeletion()
	}

	deletionDate := float64(now.Unix())
	if force {
		if aerr := h.store.deleteSecret(ctx, sec.Name); aerr != nil {
			return nil, aerr
		}
	} else {
		deletionDate = float64(now.Add(time.Duration(window) * 24 * time.Hour).Unix())
		sec.DeletedDate = deletionDate
		sec.LastChangedDate = float64(now.Unix())
		if aerr := h.store.putSecret(ctx, sec); aerr != nil {
			return nil, aerr
		}
	}

	h.publishCtx(ctx, events.SecretDeleted, events.ResourcePayload{Name: sec.Name, ARN: sec.ARN})
	log.Info("secret deleted", zap.String("name", sec.Name), zap.Bool("forced", force))
	return &deleteSecretResponse{
		ARN:          sec.ARN,
		Name:         sec.Name,
		DeletionDate: deletionDate,
	}, nil
}

// restoreSecretTyped cancels a scheduled deletion by clearing DeletedDate. AWS
// accepts it for a secret that was never scheduled too, and answers with the
// secret's identity either way; only a secret whose window has already closed
// is gone, and that one is a ResourceNotFoundException.
func (h *Handler) restoreSecretTyped(ctx context.Context, req *secretIDRequest) (*restoreSecretResponse, *protocol.AWSError) {
	sec, aerr := h.store.resolveSecret(ctx, req.SecretId)
	if aerr != nil {
		return nil, aerr
	}
	if sec.DeletedDate != 0 {
		sec.DeletedDate = 0
		sec.LastChangedDate = float64(h.store.now().Unix())
		if aerr := h.store.putSecret(ctx, sec); aerr != nil {
			return nil, aerr
		}
		h.publishCtx(ctx, events.SecretUpdated, events.ResourcePayload{Name: sec.Name, ARN: sec.ARN})
		h.log.WithRecorder(ctx).Info("secret restored", zap.String("name", sec.Name))
	}
	return &restoreSecretResponse{ARN: sec.ARN, Name: sec.Name}, nil
}

// tagResourceTyped merges tags through serviceutil.ApplyInlineTags, which
// validates the merged set (existing plus incoming) before saving — Secret
// already implements serviceutil.Taggable (store.go's GetTags/SetTags), and
// SetTags already renders the result key-sorted, so there is no ordering
// change here versus the hand-rolled merge this replaces (#1052).
func (h *Handler) tagResourceTyped(ctx context.Context, req *tagResourceRequest) (*struct{}, *protocol.AWSError) {
	if aerr := serviceutil.ApplyInlineTags(ctx, req.SecretId, smTagsToMap(req.Tags), smTagCfg,
		h.resolveLiveSecret, h.store.putSecret); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (h *Handler) cancelRotateSecretTyped(ctx context.Context, req *secretIDRequest) (*cancelRotateSecretResponse, *protocol.AWSError) {
	sec, aerr := h.resolveLiveSecret(ctx, req.SecretId)
	if aerr != nil {
		return nil, aerr
	}
	// AWS turns rotation off but leaves the function and rules configured, so
	// that a later RotateSecret can turn it straight back on. It also warns
	// that a cancelled in-flight rotation may leave AWSPENDING attached — we
	// mirror that by leaving the staging labels exactly as they are.
	sec.RotationEnabled = false
	sec.NextRotationDate = 0
	sec.LastChangedDate = float64(h.store.now().Unix())
	if aerr := h.store.putSecret(ctx, sec); aerr != nil {
		return nil, aerr
	}
	out := &cancelRotateSecretResponse{ARN: sec.ARN, Name: sec.Name}
	if pending := sec.versionByStage(stageAWSPending); pending != nil {
		out.VersionId = pending.VersionId
	}
	return out, nil
}

func (h *Handler) untagResourceTyped(ctx context.Context, req *untagResourceRequest) (*struct{}, *protocol.AWSError) {
	sec, aerr := h.resolveLiveSecret(ctx, req.SecretId)
	if aerr != nil {
		return nil, aerr
	}
	remove := make(map[string]struct{}, len(req.TagKeys))
	for _, k := range req.TagKeys {
		remove[k] = struct{}{}
	}
	filtered := sec.Tags[:0]
	for _, t := range sec.Tags {
		if _, drop := remove[t.Key]; !drop {
			filtered = append(filtered, t)
		}
	}
	sec.Tags = filtered
	if aerr := h.store.putSecret(ctx, sec); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (h *Handler) getRandomPasswordTyped(_ context.Context, req *getRandomPasswordRequest) (*getRandomPasswordResponse, *protocol.AWSError) {
	password, aerr := generatePassword(req)
	if aerr != nil {
		return nil, aerr
	}
	return &getRandomPasswordResponse{RandomPassword: password}, nil
}

// generatePassword builds one password to the request's exclusion settings.
// CloudFormation's AWS::SecretsManager::Secret GenerateSecretString takes the
// same knobs, and reaches this through GetRandomPassword rather than carrying a
// second generator.
func generatePassword(req *getRandomPasswordRequest) (string, *protocol.AWSError) {
	length := int64(defaultPasswordLength)
	if req.PasswordLength != nil {
		length = *req.PasswordLength
		if length < 1 || length > 4096 {
			return "", errInvalidParameter("PasswordLength must be between 1 and 4096.")
		}
	}
	if utf8.RuneCountInString(req.ExcludeCharacters) > 4096 {
		return "", errInvalidParameter("ExcludeCharacters exceeds the maximum length of 4096 characters.")
	}
	charset := allowedPasswordCharset(req)
	if len(charset) == 0 {
		return "", errInvalidParameter("No characters available with the given exclusion settings.")
	}

	password := make([]rune, int(length))
	for i := range password {
		n, err := randIndex(len(charset))
		if err != nil {
			return "", protocol.Wrap(protocol.ErrInternalError, err)
		}
		password[i] = charset[n]
	}
	// AWS defaults RequireEachIncludedType to true: "If you don't include this
	// switch, the password contains at least one of every character type."
	if req.RequireEachIncludedType == nil || *req.RequireEachIncludedType {
		if err := seedEachIncludedType(password, charset); err != nil {
			return "", protocol.Wrap(protocol.ErrInternalError, err)
		}
	}
	return string(password), nil
}

// seedEachIncludedType overwrites distinct positions so the password holds at
// least one character of every type the exclusions left available. Positions
// are distinct so seeding one type never undoes another; if there are fewer
// positions than types — possible for valid PasswordLength values 1 through
// 3 — the leftover types go unseeded rather than fighting over a slot.
func seedEachIncludedType(password, charset []rune) error {
	types := includedCharTypes(charset)
	if len(types) == 0 {
		return nil
	}
	positions := make([]int, len(password))
	for i := range positions {
		positions[i] = i
	}
	for i := len(positions) - 1; i > 0; i-- {
		j, err := randIndex(i + 1)
		if err != nil {
			return err
		}
		positions[i], positions[j] = positions[j], positions[i]
	}
	for i, chars := range types {
		if i >= len(positions) {
			break
		}
		j, err := randIndex(len(chars))
		if err != nil {
			return err
		}
		password[positions[i]] = chars[j]
	}
	return nil
}

// includedCharTypes partitions an already-filtered charset into the four types
// AWS names — uppercase, lowercase, number, punctuation — dropping any the
// exclusions emptied. A space is not one of them: IncludeSpace widens the
// alphabet without adding a type that must appear.
func includedCharTypes(charset []rune) [][]rune {
	var upper, lower, digit, punct []rune
	for _, c := range charset {
		switch {
		case c >= 'A' && c <= 'Z':
			upper = append(upper, c)
		case c >= 'a' && c <= 'z':
			lower = append(lower, c)
		case c >= '0' && c <= '9':
			digit = append(digit, c)
		case c == ' ':
		default:
			punct = append(punct, c)
		}
	}
	types := make([][]rune, 0, 4)
	for _, chars := range [][]rune{upper, lower, digit, punct} {
		if len(chars) > 0 {
			types = append(types, chars)
		}
	}
	return types
}

// randIndex returns a uniform random index in [0, n).
func randIndex(n int) (int, error) {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, err
	}
	return int(v.Int64()), nil
}

func (h *Handler) batchGetSecretValueTyped(ctx context.Context, req *batchGetSecretValueRequest) (*batchGetSecretValueResponse, *protocol.AWSError) {
	// AWS: "You must include Filters or SecretIdList, but not both."
	if len(req.Filters) > 0 && len(req.SecretIdList) > 0 {
		return nil, errInvalidParameter("You must include Filters or SecretIdList, but not both.")
	}

	ids := req.SecretIdList
	if len(req.Filters) > 0 {
		if aerr := validateSecretFilters(req.Filters); aerr != nil {
			return nil, aerr
		}
		// The filter form selects the secrets to fetch, exactly as ListSecrets
		// does. Ignoring it would return an empty result for a request AWS
		// answers — a successful lookup that found nothing, which is worse than
		// a 501.
		secrets, aerr := h.store.listSecrets(ctx)
		if aerr != nil {
			return nil, aerr
		}
		now := h.store.now()
		for _, sec := range secrets {
			if secretMatchesFilters(sec, req.Filters) && !sec.markedForDeletion(now) {
				ids = append(ids, sec.Name)
			}
		}
	}

	out := &batchGetSecretValueResponse{
		SecretValues: make([]secretValueResponse, 0),
		Errors:       make([]batchSecretError, 0),
	}
	for _, id := range ids {
		sec, aerr := h.resolveLiveSecret(ctx, id)
		if aerr != nil {
			out.Errors = append(out.Errors, batchSecretError{
				SecretId:  id,
				ErrorCode: aerr.Code,
				Message:   aerr.Message,
			})
			continue
		}
		version := findSecretVersion(sec, "")
		if version == nil {
			out.Errors = append(out.Errors, batchSecretError{
				SecretId:  id,
				ErrorCode: "ResourceNotFoundException",
				Message:   fmt.Sprintf("secret %q has no current version", id),
			})
			continue
		}
		out.SecretValues = append(out.SecretValues, *secretValueOut(sec, version))
	}
	return out, nil
}

func findSecretVersion(sec *Secret, versionId string) *SecretVersion {
	if versionId == "" {
		return sec.currentVersion()
	}
	return sec.versionByID(versionId)
}

func secretValueOut(sec *Secret, version *SecretVersion) *secretValueResponse {
	return &secretValueResponse{
		ARN:           sec.ARN,
		Name:          sec.Name,
		VersionId:     version.VersionId,
		VersionStages: version.Stages,
		SecretString:  version.SecretString,
		SecretBinary:  version.SecretBinary,
		CreatedDate:   version.CreatedDate,
	}
}

func allowedPasswordCharset(req *getRandomPasswordRequest) []rune {
	excluded := make(map[rune]struct{})
	for _, c := range req.ExcludeCharacters {
		excluded[c] = struct{}{}
	}
	digits := "0123456789"
	punctuation := "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"
	uppercase := "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	lowercase := "abcdefghijklmnopqrstuvwxyz"

	var charset []rune
	for _, c := range defaultPasswordChars {
		if _, skip := excluded[c]; skip {
			continue
		}
		if req.ExcludeNumbers && containsRune(digits, c) {
			continue
		}
		if req.ExcludePunctuation && containsRune(punctuation, c) {
			continue
		}
		if req.ExcludeUppercase && containsRune(uppercase, c) {
			continue
		}
		if req.ExcludeLowercase && containsRune(lowercase, c) {
			continue
		}
		charset = append(charset, c)
	}
	if req.IncludeSpace {
		charset = append(charset, ' ')
	}
	return charset
}
