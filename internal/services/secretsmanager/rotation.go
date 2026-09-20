package secretsmanager

// rotation.go — the AWS four-step rotation protocol.
//
// Real Secrets Manager drives rotation by invoking a Lambda function four
// times, handing it the same ClientRequestToken each time and a Step naming
// where in the protocol it is. The function does the work:
//
//	createSecret — generate the new value and PutSecretValue it staged AWSPENDING
//	setSecret    — apply the new value to the target system (a database, say)
//	testSecret   — verify the new value works
//	finishSecret — UpdateSecretVersionStage to move AWSCURRENT onto the new version
//
// Overcast runs the same sequence against the local Lambda emulator, in order,
// waiting for each step before starting the next. Two deliberate divergences
// from AWS, both recorded in docs/services/secretsmanager.md:
//
//  1. RotateSecret runs the sequence inline and returns only once it has
//     finished, where AWS returns immediately and rotates in the background.
//     Locally, a deterministic "it has rotated by the time the call returns" is
//     worth more than reproducing the asynchrony, and it is what makes the
//     failure visible at all.
//  2. A failed step fails the RotateSecret call with InvalidRequestException
//     naming the step. AWS answers 200 and surfaces the failure through
//     CloudTrail and the console, which a local dev loop has no equivalent of.
//     Reporting success for a rotation that did not happen is the §2.1 shape-2
//     failure this work exists to remove.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// The four steps, in the order Secrets Manager runs them.
const (
	stepCreateSecret = "createSecret"
	stepSetSecret    = "setSecret"
	stepTestSecret   = "testSecret"
	stepFinishSecret = "finishSecret"
)

// rotationSteps is the protocol's fixed sequence. Running a subset would be
// the "looks like it rotated" failure mode, so there is exactly one list.
var rotationSteps = []string{stepCreateSecret, stepSetSecret, stepTestSecret, stepFinishSecret}

// Rotation attempt statuses recorded on the secret.
const (
	rotationSucceeded = "Succeeded"
	rotationFailed    = "Failed"
)

// Rotation triggers, for the attempt record.
const (
	triggerRotateSecret = "RotateSecret"
	triggerScheduled    = "Scheduled"
)

// AWS's bounds on RotationRules.AutomaticallyAfterDays.
const (
	minRotationDays = 1
	maxRotationDays = 1000
)

// rotationEvent is the payload Secrets Manager sends a rotation function.
// The field names are AWS's and the rotation-function blueprints read them
// verbatim, so they are not ours to rename.
type rotationEvent struct {
	Step               string `json:"Step"`
	SecretId           string `json:"SecretId"`
	ClientRequestToken string `json:"ClientRequestToken"`
}

// ─── RotateSecret ──────────────────────────────────────────────────────────

func (h *Handler) rotateSecretTyped(ctx context.Context, req *rotateSecretRequest) (*rotateSecretResponse, *protocol.AWSError) {
	sec, aerr := h.resolveLiveSecret(ctx, req.SecretId)
	if aerr != nil {
		return nil, aerr
	}

	lambdaARN := req.RotationLambdaARN
	if lambdaARN == "" {
		lambdaARN = sec.RotationLambdaARN
	}
	if lambdaARN == "" {
		// AWS: "You tried to enable rotation on a secret that doesn't already
		// have a Lambda function ARN configured and you didn't include such an
		// ARN as a parameter in this call."
		return nil, errInvalidRequest(
			"You must specify a Lambda rotation function ARN, either on this call or already configured on the secret.")
	}
	if req.RotationRules != nil {
		if aerr := validateRotationRules(req.RotationRules); aerr != nil {
			return nil, aerr
		}
	}

	now := h.store.now()
	sec.RotationEnabled = true
	sec.RotationLambdaARN = lambdaARN
	if req.RotationRules != nil {
		sec.RotationRules = req.RotationRules
	}
	sec.LastChangedDate = float64(now.Unix())
	sec.NextRotationDate = nextRotationDate(now, sec.RotationRules)
	if aerr := h.store.putSecret(ctx, sec); aerr != nil {
		return nil, aerr
	}
	h.wakeRotationEngine()

	rotateNow := req.RotateImmediately == nil || *req.RotateImmediately
	if !rotateNow {
		return &rotateSecretResponse{ARN: sec.ARN, Name: sec.Name, VersionId: sec.CurrentVersionId}, nil
	}

	token := req.ClientRequestToken
	if token == "" {
		token = uuid.New().String()
	}
	if aerr := h.runRotation(ctx, sec.Name, token, triggerRotateSecret); aerr != nil {
		return nil, aerr
	}
	return &rotateSecretResponse{ARN: sec.ARN, Name: sec.Name, VersionId: token}, nil
}

func validateRotationRules(rules *RotationRules) *protocol.AWSError {
	if rules.AutomaticallyAfterDays == 0 {
		return nil
	}
	if rules.AutomaticallyAfterDays < minRotationDays || rules.AutomaticallyAfterDays > maxRotationDays {
		return errInvalidParameter(fmt.Sprintf(
			"RotationRules.AutomaticallyAfterDays must be between %d and %d.", minRotationDays, maxRotationDays))
	}
	return nil
}

// nextRotationDate computes when the schedule next fires, as a Unix timestamp.
// Zero means "no schedule": AWS omits NextRotationDate for a secret rotated
// only on demand.
func nextRotationDate(now time.Time, rules *RotationRules) float64 {
	if rules == nil {
		return 0
	}
	if days := rules.AutomaticallyAfterDays; days > 0 {
		return float64(now.AddDate(0, 0, int(days)).Unix())
	}
	if d, ok := parseRotationRate(rules.ScheduleExpression); ok {
		return float64(now.Add(d).Unix())
	}
	// cron() expressions are accepted and stored, but Overcast does not
	// evaluate them: a schedule it cannot compute is left with no next date
	// rather than an invented one.
	return 0
}

// parseRotationRate understands the rate(N unit) form of ScheduleExpression.
func parseRotationRate(expr string) (time.Duration, bool) {
	expr = strings.TrimSpace(expr)
	if !strings.HasPrefix(expr, "rate(") || !strings.HasSuffix(expr, ")") {
		return 0, false
	}
	fields := strings.Fields(expr[len("rate(") : len(expr)-1])
	if len(fields) != 2 {
		return 0, false
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil || n <= 0 {
		return 0, false
	}
	switch strings.TrimSuffix(strings.ToLower(fields[1]), "s") {
	case "hour":
		return time.Duration(n) * time.Hour, true
	case "day":
		return time.Duration(n) * 24 * time.Hour, true
	default:
		return 0, false
	}
}

// ─── The step sequence ─────────────────────────────────────────────────────

// runRotation drives all four steps for one secret under one token.
//
// The secret is re-read from the store between steps because the rotation
// function mutates it through the ordinary API — that is the whole point of
// the protocol, and holding a stale copy across the sequence would silently
// undo the function's work on the next write.
func (h *Handler) runRotation(ctx context.Context, secretName, token, trigger string) *protocol.AWSError {
	// One rotation at a time per process: a scheduled sweep must not start a
	// second sequence over a secret an API call is already rotating, or the two
	// fight over AWSPENDING.
	h.rotationMu.Lock()
	defer h.rotationMu.Unlock()

	sec, aerr := h.store.resolveSecret(ctx, secretName)
	if aerr != nil {
		return aerr
	}
	started := h.store.now()
	attempt := &RotationAttempt{
		ClientRequestToken: token,
		Trigger:            trigger,
		StartedDate:        float64(started.Unix()),
	}
	originalCurrent := sec.CurrentVersionId
	functionName := lambdaFunctionName(sec.RotationLambdaARN)

	// Stage the token before the first invocation, not after it: AWS's
	// rotation blueprints refuse to run createSecret for a token that is not
	// already staged AWSPENDING.
	attempt.Step = stepCreateSecret
	if aerr := h.stagePendingRotationVersion(ctx, sec, token); aerr != nil {
		return h.recordRotationFailure(ctx, secretName, attempt,
			"could not stage version "+token+" as "+stageAWSPending+": "+aerr.Message)
	}

	for _, step := range rotationSteps {
		attempt.Step = step
		if err := h.invokeRotationStep(ctx, functionName, sec.ARN, step, token); err != nil {
			return h.recordRotationFailure(ctx, secretName, attempt, err.Error())
		}
	}

	// finishSecret is the step that is supposed to move AWSCURRENT. A function
	// that returns success without doing so has not rotated the secret, and
	// saying otherwise is exactly the failure §2.1 shape 2 describes.
	after, aerr := h.store.resolveSecret(ctx, secretName)
	if aerr != nil {
		return aerr
	}
	if after.CurrentVersionId == originalCurrent || after.CurrentVersionId != token {
		return h.recordRotationFailure(ctx, secretName, attempt,
			"the rotation function completed "+stepFinishSecret+" without moving "+stageAWSCurrent+
				" to version "+token)
	}

	now := h.store.now()
	after.detachStage(stageAWSPending)
	after.LastRotatedDate = float64(now.Unix())
	after.NextRotationDate = nextRotationDate(now, after.RotationRules)
	after.LastChangedDate = float64(now.Unix())
	attempt.Status = rotationSucceeded
	attempt.CompletedDate = float64(now.Unix())
	after.LastRotationAttempt = attempt
	if aerr := h.store.putSecret(ctx, after); aerr != nil {
		return aerr
	}
	// Published here rather than at the RotateSecret call site so a scheduled
	// rotation shows up on the event bus too — the console's feed should not
	// depend on who started the rotation.
	h.publishCtx(ctx, events.SecretRotated, events.ResourcePayload{Name: after.Name, ARN: after.ARN})
	h.log.Info("secret rotated",
		zap.String("name", secretName),
		zap.String("version", token),
		zap.String("trigger", trigger),
	)
	return nil
}

// stagePendingRotationVersion makes the rotation's ClientRequestToken a version
// of the secret, staged AWSPENDING, before the rotation function is called.
//
// This is Secrets Manager's own work, not the function's. AWS stages the token
// first and leaves the version empty; createSecret is what puts a value in it.
// The blueprints AWS publishes — the generic SecretsManagerRotationTemplate and
// the RDS, MySQL, PostgreSQL and Redshift ones — all open lambda_handler by
// asserting exactly that, for *every* step including createSecret:
//
//	versions = describe_secret(SecretId=arn)['VersionIdsToStages']
//	if token not in versions:      raise "…has no stage for rotation of secret…"
//	elif "AWSPENDING" not in versions[token]: raise "…not set as AWSPENDING…"
//
// so a rotation function built from one of them fails on its first invocation
// against an emulator that does not stage the token first. Two other pieces of
// AWS's own documentation say the same thing from the other direction: a
// RotateSecret with RotateImmediately=false "creates an AWSPENDING version of
// the secret and then removes it" while running only testSecret, and a failed
// rotation can leave "the AWSPENDING staging label attached to an empty secret
// version" — neither is reachable if the function is what creates the version.
//
// Re-staging a token that is already pending is a no-op, so a retried rotation
// under the same token finds the version it left behind, exactly as AWS's
// idempotency contract intends.
func (h *Handler) stagePendingRotationVersion(ctx context.Context, sec *Secret, token string) *protocol.AWSError {
	if v := sec.versionByID(token); v == nil {
		sec.Versions = append(sec.Versions, SecretVersion{
			VersionId:   token,
			Stages:      []string{},
			CreatedDate: float64(h.store.now().Unix()),
		})
	} else if containsString(v.Stages, stageAWSPending) {
		return nil
	}
	sec.attachStage(stageAWSPending, token)
	return h.store.putSecret(ctx, sec)
}

// invokeRotationStep invokes the rotation function once, synchronously.
//
// The synchronous seam (events.FunctionSyncInvoker, the same one API Gateway
// and AppSync use) is the only one that fits: the protocol is sequenced, so
// each step's result has to be known before the next begins. The asynchronous
// InvokeAsync path answers before the function has run and swallows its error.
func (h *Handler) invokeRotationStep(ctx context.Context, functionName, secretARN, step, token string) error {
	if h.invoker == nil {
		return fmt.Errorf("no Lambda invoker is wired into the Secrets Manager service")
	}
	payload, err := json.Marshal(rotationEvent{Step: step, SecretId: secretARN, ClientRequestToken: token})
	if err != nil {
		return fmt.Errorf("build %s payload: %w", step, err)
	}
	outcome, err := h.invoker.Invoke(ctx, functionName, payload)
	if err != nil {
		return fmt.Errorf("%s: %w", step, err)
	}
	if outcome == nil {
		return fmt.Errorf("%s: the rotation function %q could not be invoked — check that it exists and is Active", step, functionName)
	}
	if outcome.FunctionError != "" {
		return fmt.Errorf("%s: the rotation function returned an error (%s): %s",
			step, outcome.FunctionError, strings.TrimSpace(string(outcome.Payload)))
	}
	return nil
}

// recordRotationFailure persists the failed attempt and turns it into the error
// the caller sees. The secret's staging labels are left exactly as the function
// left them: AWS documents the same, and clearing them would destroy the
// evidence needed to recover the rotation by hand.
func (h *Handler) recordRotationFailure(ctx context.Context, secretName string, attempt *RotationAttempt, msg string) *protocol.AWSError {
	attempt.Status = rotationFailed
	attempt.Error = msg
	attempt.CompletedDate = float64(h.store.now().Unix())

	if sec, aerr := h.store.resolveSecret(ctx, secretName); aerr == nil {
		sec.LastRotationAttempt = attempt
		if aerr := h.store.putSecret(ctx, sec); aerr != nil {
			h.log.Warn("failed to record rotation attempt", zap.String("name", secretName), zap.String("error", aerr.Message))
		}
	}
	h.log.Warn("secret rotation failed",
		zap.String("name", secretName),
		zap.String("step", attempt.Step),
		zap.String("reason", msg),
	)
	return errInvalidRequest("Rotation of secret " + secretName + " failed at step " + attempt.Step + ": " + msg)
}

// lambdaFunctionName reduces a function ARN to the bare name the sync invoker
// expects. A bare name passes through unchanged.
func lambdaFunctionName(arnOrName string) string {
	const marker = ":function:"
	if idx := strings.Index(arnOrName, marker); idx >= 0 {
		name := arnOrName[idx+len(marker):]
		// Strip a version or alias qualifier.
		if colon := strings.Index(name, ":"); colon >= 0 {
			name = name[:colon]
		}
		return name
	}
	return arnOrName
}

// ─── Scheduled rotation ────────────────────────────────────────────────────

const (
	// rotationEngineMaxSleep caps a single sleep so that a schedule years out
	// does not park a timer for years. It is not a polling interval — with
	// nothing scheduled the engine holds no timer at all.
	rotationEngineMaxSleep = time.Hour

	// rotationEngineBackoff is how long the engine waits after a sweep that
	// found overdue work but rotated none of it. Without it, a schedule whose
	// rotation function is permanently broken would spin the loop.
	rotationEngineBackoff = time.Minute
)

// startRotationEngine launches the single background loop that fires scheduled
// rotations. One goroutine for the whole service: no goroutine per secret, no
// timer per secret. It sleeps until the earliest due rotation, and when nothing
// is scheduled it holds no timer and simply waits to be woken — so a server
// with no rotation configured pays nothing for the feature existing.
func (s *Service) startRotationEngine() {
	s.engineOnce.Do(func() {
		s.wg.Add(1)
		go s.runRotationEngine()
	})
}

func (s *Service) runRotationEngine() {
	defer s.wg.Done()
	for {
		wait, scheduled := s.untilNextRotation()
		if !scheduled {
			// Nothing to wait for. RotateSecret wakes us when that changes.
			select {
			case <-s.stopCh:
				return
			case <-s.handler.rotationWake:
			}
			continue
		}

		if wait <= 0 {
			// Already due. Sweep now rather than arming a zero-length timer.
			if s.handler.rotateDueSecrets(context.Background()) > 0 {
				continue
			}
			// Overdue work that would not rotate: back off rather than spin.
			wait = rotationEngineBackoff
		}

		timer := s.handler.clk.Timer(wait)
		select {
		case <-s.stopCh:
			timer.Stop()
			return
		case <-s.handler.rotationWake:
			timer.Stop()
		case <-timer.C:
			s.handler.rotateDueSecrets(context.Background())
		}
	}
}

// untilNextRotation returns how long the engine may sleep and whether anything
// is scheduled at all. A schedule already overdue returns a zero duration so
// the sweep runs on the next turn of the loop.
func (s *Service) untilNextRotation() (time.Duration, bool) {
	earliest := s.handler.earliestRotationDate(context.Background())
	if earliest.IsZero() {
		return 0, false
	}
	wait := earliest.Sub(s.handler.clk.Now())
	if wait <= 0 {
		return 0, true
	}
	return min(wait, rotationEngineMaxSleep), true
}

// Stop shuts the rotation engine down and waits for it to drain.
func (s *Service) Stop(ctx context.Context) {
	s.stopOnce.Do(func() { close(s.stopCh) })
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
	case <-done:
	}
}

// wakeRotationEngine nudges the loop to recompute its sleep after a schedule
// changed. It never blocks: the channel is buffered depth 1 and a pending wake
// already achieves the same thing.
func (h *Handler) wakeRotationEngine() {
	select {
	case h.rotationWake <- struct{}{}:
	default:
	}
}

// earliestRotationDate returns the soonest scheduled rotation across every
// region, or the zero time when nothing is scheduled.
func (h *Handler) earliestRotationDate(ctx context.Context) time.Time {
	var earliest time.Time
	h.forEachScheduledSecret(ctx, func(_ string, sec *Secret) {
		at := time.Unix(int64(sec.NextRotationDate), 0)
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
	})
	return earliest
}

// rotateDueSecrets rotates every secret whose schedule has come due and
// returns how many rotated. Failures are logged and recorded on the secret;
// one failing rotation never stops the others.
func (h *Handler) rotateDueSecrets(ctx context.Context) int {
	now := h.store.now()
	type due struct {
		region string
		name   string
	}
	var pending []due
	h.forEachScheduledSecret(ctx, func(region string, sec *Secret) {
		if time.Unix(int64(sec.NextRotationDate), 0).After(now) {
			return
		}
		pending = append(pending, due{region: region, name: sec.Name})
	})

	rotated := 0
	for _, d := range pending {
		regionCtx := middleware.ContextWithRegion(ctx, d.region)
		if aerr := h.runRotation(regionCtx, d.name, uuid.New().String(), triggerScheduled); aerr != nil {
			// A failed scheduled rotation waits for the next window rather than
			// retrying at once — which is also what stops the engine spinning
			// on a permanently broken rotation function. The failure is already
			// recorded on the secret and in the log.
			h.deferNextRotation(regionCtx, d.name)
			continue
		}
		rotated++
	}
	return rotated
}

// deferNextRotation moves a secret's NextRotationDate to the next scheduled
// window, leaving everything else — including the recorded failure — alone.
func (h *Handler) deferNextRotation(ctx context.Context, secretName string) {
	sec, aerr := h.store.resolveSecret(ctx, secretName)
	if aerr != nil {
		return
	}
	next := nextRotationDate(h.store.now(), sec.RotationRules)
	if next == 0 {
		// No computable schedule: turn the schedule off rather than leave a
		// date in the past that the sweep would pick up on every pass.
		sec.NextRotationDate = 0
	} else {
		sec.NextRotationDate = next
	}
	if aerr := h.store.putSecret(ctx, sec); aerr != nil {
		h.log.Warn("rotation sweep: could not defer next rotation",
			zap.String("name", secretName), zap.String("error", aerr.Message))
	}
}

// forEachScheduledSecret visits every secret with rotation enabled and a
// schedule, across all regions.
//
// This is the only full scan the rotation feature performs, and it runs on the
// engine goroutine — never on a read path. GetSecretValue and friends pay
// nothing for rotation existing.
func (h *Handler) forEachScheduledSecret(ctx context.Context, visit func(region string, sec *Secret)) {
	secrets, aerr := h.store.listSecretsAllRegions(ctx)
	if aerr != nil {
		h.log.Warn("rotation sweep: scan secrets", zap.String("error", aerr.Message))
		return
	}
	for i := range secrets {
		sec := &secrets[i].Secret
		if !sec.RotationEnabled || sec.NextRotationDate == 0 || sec.RotationLambdaARN == "" {
			continue
		}
		region := secrets[i].Region
		if region == "" {
			region = h.cfg.Region
		}
		visit(region, sec)
	}
}
