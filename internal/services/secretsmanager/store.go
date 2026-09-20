package secretsmanager

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const (
	nsSecrets = "secretsmanager:secrets"

	// Staging labels. AWS reserves these three; callers may attach their own.
	stageAWSCurrent  = "AWSCURRENT"
	stageAWSPending  = "AWSPENDING"
	stageAWSPrevious = "AWSPREVIOUS"

	// arnSuffixLen is the length of the random suffix AWS appends to every
	// secret ARN (e.g. …:secret:prod/db-password-a1B2c3). Callers may address a
	// secret by the "partial ARN" that omits it, which resolveSecret honours.
	arnSuffixLen = 6

	// Recovery window bounds for DeleteSecret. AWS's API reference gives the
	// range as 7 to 30 days, and 30 days when the caller names neither
	// RecoveryWindowInDays nor ForceDeleteWithoutRecovery.
	minRecoveryWindowDays     = 7
	maxRecoveryWindowDays     = 30
	defaultRecoveryWindowDays = 30

	// maxUnlabeledVersions caps how many staging-label-free versions are kept.
	// AWS deprecates unlabelled versions once a secret exceeds 100 of them and
	// never removes a labelled one; the cap here is smaller because a local
	// emulator has no reason to hold more, but the labelled-versions-survive
	// rule is the part that matters — pruning a staged version mid-rotation
	// would break the protocol.
	maxUnlabeledVersions = 2
)

// Tag represents a key-value tag on a secret.
type Tag struct {
	Key   string `json:"Key" cbor:"Key"`
	Value string `json:"Value" cbor:"Value"`
}

// SecretVersion holds the payload for one version of a secret.
type SecretVersion struct {
	VersionId    string   `json:"VersionId" cbor:"VersionId"`
	SecretString string   `json:"SecretString,omitempty" cbor:"SecretString,omitempty"`
	SecretBinary string   `json:"SecretBinary,omitempty" cbor:"SecretBinary,omitempty"` // base64-encoded
	Stages       []string `json:"VersionStages" cbor:"VersionStages"`
	CreatedDate  float64  `json:"CreatedDate" cbor:"CreatedDate"`
}

// RotationRules describes the automatic rotation schedule.
type RotationRules struct {
	AutomaticallyAfterDays int64  `json:"AutomaticallyAfterDays,omitempty" cbor:"AutomaticallyAfterDays,omitempty"`
	Duration               string `json:"Duration,omitempty" cbor:"Duration,omitempty"`
	ScheduleExpression     string `json:"ScheduleExpression,omitempty" cbor:"ScheduleExpression,omitempty"`
}

// RotationAttempt records the outcome of the most recent rotation run.
//
// It has no AWS API equivalent — real Secrets Manager surfaces rotation
// progress through CloudTrail and the console. Overcast keeps it on the secret
// so the web console can show which of the four steps failed, and so a failure
// is still visible after the RotateSecret call that reported it has gone. It is
// deliberately absent from every AWS-shaped response.
type RotationAttempt struct {
	ClientRequestToken string  `json:"ClientRequestToken,omitempty"`
	Status             string  `json:"Status,omitempty"`
	Step               string  `json:"Step,omitempty"`
	Error              string  `json:"Error,omitempty"`
	Trigger            string  `json:"Trigger,omitempty"`
	StartedDate        float64 `json:"StartedDate,omitempty"`
	CompletedDate      float64 `json:"CompletedDate,omitempty"`
}

// Secret is the full domain model stored for each secret.
type Secret struct {
	ARN                 string           `json:"ARN"`
	Name                string           `json:"Name"`
	Description         string           `json:"Description,omitempty"`
	KmsKeyId            string           `json:"KmsKeyId,omitempty"`
	Tags                []Tag            `json:"Tags,omitempty"`
	Versions            []SecretVersion  `json:"Versions"`
	CurrentVersionId    string           `json:"CurrentVersionId"`
	CreatedDate         float64          `json:"CreatedDate"`
	LastChangedDate     float64          `json:"LastChangedDate"`
	RotationEnabled     bool             `json:"RotationEnabled,omitempty"`
	RotationRules       *RotationRules   `json:"RotationRules,omitempty"`
	RotationLambdaARN   string           `json:"RotationLambdaARN,omitempty"`
	LastRotatedDate     float64          `json:"LastRotatedDate,omitempty"`
	NextRotationDate    float64          `json:"NextRotationDate,omitempty"`
	LastRotationAttempt *RotationAttempt `json:"LastRotationAttempt,omitempty"`
	ResourcePolicy      string           `json:"ResourcePolicy,omitempty"`

	// DeletedDate is the end of the recovery window DeleteSecret opened: the
	// instant after which the secret is gone for good. Zero means the secret is
	// live. AWS names the same value DeletionDate in DeleteSecret's response and
	// DeletedDate everywhere it is read back, and so does Overcast.
	DeletedDate float64 `json:"DeletedDate,omitempty"`
}

// markedForDeletion reports whether the secret is inside a recovery window:
// scheduled for deletion, still restorable, and refusing value operations.
func (s *Secret) markedForDeletion(now time.Time) bool {
	return s.DeletedDate != 0 && float64(now.Unix()) < s.DeletedDate
}

// recoveryWindowElapsed reports whether the secret's recovery window has run
// out, which is the moment AWS deletes it permanently.
func (s *Secret) recoveryWindowElapsed(now time.Time) bool {
	return s.DeletedDate != 0 && float64(now.Unix()) >= s.DeletedDate
}

func (s *Secret) GetTags() map[string]string {
	out := make(map[string]string, len(s.Tags))
	for _, t := range s.Tags {
		out[t.Key] = t.Value
	}
	return out
}

// SetTags stores the tags key-sorted, because this slice is what the wire sees:
// a secret's tags are rendered in the order they are held, so an unordered
// write here is a response that reorders between identical calls.
func (s *Secret) SetTags(tags map[string]string) {
	s.Tags = serviceutil.TagElements(tags, func(k, v string) Tag {
		return Tag{Key: k, Value: v}
	})
}

// currentVersion returns the version carrying AWSCURRENT, or nil.
func (s *Secret) currentVersion() *SecretVersion { return s.versionByStage(stageAWSCurrent) }

// versionByStage returns the version carrying stage, or nil when none does.
func (s *Secret) versionByStage(stage string) *SecretVersion {
	for i := range s.Versions {
		for _, st := range s.Versions[i].Stages {
			if st == stage {
				return &s.Versions[i]
			}
		}
	}
	return nil
}

// versionByID returns the version with the given ID, or nil.
func (s *Secret) versionByID(id string) *SecretVersion {
	for i := range s.Versions {
		if s.Versions[i].VersionId == id {
			return &s.Versions[i]
		}
	}
	return nil
}

// detachStage removes stage from whichever version currently holds it and
// returns that version's ID (empty when no version held it).
func (s *Secret) detachStage(stage string) string {
	for i := range s.Versions {
		for j, st := range s.Versions[i].Stages {
			if st != stage {
				continue
			}
			s.Versions[i].Stages = append(s.Versions[i].Stages[:j:j], s.Versions[i].Stages[j+1:]...)
			return s.Versions[i].VersionId
		}
	}
	return ""
}

// attachStage gives stage to versionID, taking it off whatever held it before.
func (s *Secret) attachStage(stage, versionID string) {
	s.detachStage(stage)
	if v := s.versionByID(versionID); v != nil {
		v.Stages = append(v.Stages, stage)
	}
}

// syncCurrentVersionID re-points CurrentVersionId at whatever holds AWSCURRENT.
// The stored field is a cache of the staging labels, and every path that moves
// AWSCURRENT has to keep it honest or GetSecretValue starts disagreeing with
// DescribeSecret.
func (s *Secret) syncCurrentVersionID() {
	if v := s.currentVersion(); v != nil {
		s.CurrentVersionId = v.VersionId
		return
	}
	s.CurrentVersionId = ""
}

// pruneVersions drops the oldest versions that carry no staging labels. A
// labelled version is never removed: AWSPENDING in particular must survive
// until the rotation that created it finishes.
func (s *Secret) pruneVersions() {
	unlabeled := 0
	for i := range s.Versions {
		if len(s.Versions[i].Stages) == 0 {
			unlabeled++
		}
	}
	if unlabeled <= maxUnlabeledVersions {
		return
	}
	drop := unlabeled - maxUnlabeledVersions
	kept := make([]SecretVersion, 0, len(s.Versions))
	for i := range s.Versions {
		if drop > 0 && len(s.Versions[i].Stages) == 0 {
			drop--
			continue
		}
		kept = append(kept, s.Versions[i])
	}
	s.Versions = kept
}

// smStore wraps state.Store with Secrets Manager specific access helpers.
type smStore struct {
	store         state.Store
	clk           clock.Clock
	defaultRegion string
}

func newSMStore(store state.Store, clk clock.Clock, defaultRegion string) *smStore {
	return &smStore{store: store, clk: clk, defaultRegion: defaultRegion}
}

// region extracts the per-request region from context, falling back to the default.
func (s *smStore) region(ctx context.Context) string {
	return middleware.RegionFromContext(ctx, s.defaultRegion)
}

func (s *smStore) getSecret(ctx context.Context, name string) (*Secret, *protocol.AWSError) {
	raw, found, err := s.store.Get(ctx, nsSecrets, serviceutil.RegionKey(s.region(ctx), name))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errResourceNotFound(name)
	}
	var sec Secret
	if err := json.Unmarshal([]byte(raw), &sec); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if sec.recoveryWindowElapsed(s.now()) {
		// The recovery window ran out, so AWS has already deleted the secret.
		// Overcast enforces that lazily rather than running a sweeper: the
		// record stays in the store, invisible to every operation, until a
		// CreateSecret for the same name overwrites it.
		return nil, errResourceNotFound(name)
	}
	return &sec, nil
}

func (s *smStore) putSecret(ctx context.Context, sec *Secret) *protocol.AWSError {
	raw, err := json.Marshal(sec)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.store.Set(ctx, nsSecrets, serviceutil.RegionKey(s.region(ctx), sec.Name), string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *smStore) deleteSecret(ctx context.Context, name string) *protocol.AWSError {
	if err := s.store.Delete(ctx, nsSecrets, serviceutil.RegionKey(s.region(ctx), name)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *smStore) listSecrets(ctx context.Context) ([]Secret, *protocol.AWSError) {
	pairs, err := s.store.Scan(ctx, nsSecrets, serviceutil.RegionKey(s.region(ctx), ""))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	now := s.now()
	out := make([]Secret, 0, len(pairs))
	for _, p := range pairs {
		var sec Secret
		if err := json.Unmarshal([]byte(p.Value), &sec); err != nil {
			continue
		}
		if sec.recoveryWindowElapsed(now) {
			continue
		}
		out = append(out, sec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// regionalSecret pairs a secret with the region its store key was scoped to.
type regionalSecret struct {
	Region string
	Secret Secret
}

// listSecretsAllRegions scans every region's secrets. Only the rotation engine
// needs this — regional handlers use listSecrets — and it runs on that engine's
// own goroutine, never on a request path.
func (s *smStore) listSecretsAllRegions(ctx context.Context) ([]regionalSecret, *protocol.AWSError) {
	pairs, err := s.store.Scan(ctx, nsSecrets, "")
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	out := make([]regionalSecret, 0, len(pairs))
	for _, p := range pairs {
		var sec Secret
		if json.Unmarshal([]byte(p.Value), &sec) != nil {
			// A single corrupt record must not blind the sweep to the rest.
			continue
		}
		if sec.DeletedDate != 0 {
			// Scheduled for deletion, or already past its window. AWS does not
			// rotate either, and a rotation would resurrect a secret the caller
			// asked to be rid of.
			continue
		}
		region, _ := serviceutil.SplitRegionKey(p.Key)
		out = append(out, regionalSecret{Region: region, Secret: sec})
	}
	return out, nil
}

// resolveSecret looks up a secret by name, full ARN, or partial ARN.
//
// AWS accepts all three, and the partial form — the ARN without its six
// character random suffix — is what people paste out of a template, so it has
// to work. Name lookups are a single store read; ARN lookups derive the name
// from the ARN rather than scanning, falling back to a scan only when neither
// candidate name resolves (which is where a secret whose own name ends in
// something suffix-shaped lands).
func (s *smStore) resolveSecret(ctx context.Context, secretId string) (*Secret, *protocol.AWSError) {
	if resource, ok := secretResourceFromARN(secretId); ok {
		for _, candidate := range []string{resource, trimARNSuffix(resource)} {
			if candidate == "" {
				continue
			}
			if sec, aerr := s.getSecret(ctx, candidate); aerr == nil && arnRefersTo(sec.ARN, secretId) {
				return sec, nil
			}
		}
		all, scanErr := s.listSecrets(ctx)
		if scanErr != nil {
			return nil, scanErr
		}
		for i := range all {
			if arnRefersTo(all[i].ARN, secretId) {
				return &all[i], nil
			}
		}
		return nil, errResourceNotFound(secretId)
	}

	sec, aerr := s.getSecret(ctx, secretId)
	if aerr == nil {
		return sec, nil
	}
	return nil, errResourceNotFound(secretId)
}

func (s *smStore) now() time.Time {
	return s.clk.Now()
}

// ─── ARN helpers ───────────────────────────────────────────────────────────

// secretARN mints an ARN in AWS's shape, including the six-character random
// suffix real Secrets Manager appends so that a deleted-and-recreated secret
// never reuses an identifier.
func secretARN(region, accountID, name string) string {
	return protocol.ARN(region, accountID, "secretsmanager", "secret:"+name+"-"+randomARNSuffix())
}

// arnSuffixAlphabet matches AWS's: mixed-case alphanumerics.
const arnSuffixAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func randomARNSuffix() string {
	out := make([]byte, arnSuffixLen)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(arnSuffixAlphabet))))
		if err != nil {
			// crypto/rand failing is not a condition a secret ARN can usefully
			// report, and a predictable suffix is still a valid ARN here.
			out[i] = arnSuffixAlphabet[i%len(arnSuffixAlphabet)]
			continue
		}
		out[i] = arnSuffixAlphabet[n.Int64()]
	}
	return string(out)
}

// secretResourceFromARN returns the part of a Secrets Manager ARN after
// ":secret:" and whether the input was such an ARN at all.
func secretResourceFromARN(id string) (string, bool) {
	if !strings.HasPrefix(id, "arn:") {
		return "", false
	}
	const marker = ":secret:"
	idx := strings.Index(id, marker)
	if idx < 0 {
		return "", false
	}
	return id[idx+len(marker):], true
}

// trimARNSuffix removes a trailing "-XXXXXX" suffix, returning "" when the
// input carries nothing suffix-shaped.
func trimARNSuffix(resource string) string {
	if len(resource) < arnSuffixLen+2 {
		return ""
	}
	cut := len(resource) - arnSuffixLen - 1
	if resource[cut] != '-' {
		return ""
	}
	for _, c := range resource[cut+1:] {
		if !strings.ContainsRune(arnSuffixAlphabet, c) {
			return ""
		}
	}
	return resource[:cut]
}

// arnRefersTo reports whether requested addresses the secret whose ARN is
// stored — either exactly, or as the partial ARN without the random suffix.
func arnRefersTo(stored, requested string) bool {
	if stored == requested {
		return true
	}
	return len(stored) == len(requested)+arnSuffixLen+1 && strings.HasPrefix(stored, requested+"-")
}

// ─── Error constructors ────────────────────────────────────────────────────

func errResourceNotFound(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ResourceNotFoundException",
		Message:    "Secrets Manager can't find the specified secret: " + name,
		HTTPStatus: 400,
	}
}

// errNoSecretValue is what AWS answers for a version that exists but holds no
// value — the state a rotation leaves behind between staging AWSPENDING and the
// function's createSecret step putting a value there. It reports whichever of
// the two identifiers the caller used to ask.
func errNoSecretValue(versionID, stage string) *protocol.AWSError {
	detail := "VersionId: " + versionID
	if stage != "" {
		detail = "staging label: " + stage
	}
	return &protocol.AWSError{
		Code:       "ResourceNotFoundException",
		Message:    "Secrets Manager can't find the specified secret value for " + detail,
		HTTPStatus: 400,
	}
}

func errResourceExists(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ResourceExistsException",
		Message:    "The operation failed because the secret " + name + " already exists.",
		HTTPStatus: 400,
	}
}

func errInvalidParameter(msg string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidParameterException",
		Message:    msg,
		HTTPStatus: 400,
	}
}

// errMarkedForDeletion is what AWS answers for every value operation on a
// secret inside its recovery window. The model lists "The secret is scheduled
// for deletion" first among InvalidRequestException's causes.
func errMarkedForDeletion() *protocol.AWSError {
	return errInvalidRequest("You can't perform this operation on the secret because it was marked for deletion.")
}

// errInvalidRequest is AWS's "valid parameters, wrong state" error — the one
// RotateSecret gives for a secret with no rotation function configured.
func errInvalidRequest(msg string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidRequestException",
		Message:    msg,
		HTTPStatus: 400,
	}
}

func errMalformedPolicy(msg string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "MalformedPolicyDocumentException",
		Message:    msg,
		HTTPStatus: 400,
	}
}
