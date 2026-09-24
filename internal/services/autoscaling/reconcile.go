package autoscaling

// The Auto Scaling reconciler.
//
// One background loop per Service — never one per group and never one per
// instance — converges every group's owned instance set on its DesiredCapacity
// by launching and terminating real EC2 instances through the emulator's own
// router, so a launch takes exactly the path an SDK client's RunInstances
// would. The same pass advances the lifecycle state machine
// (Pending → InService, Terminating → gone), releases instances whose
// lifecycle-hook heartbeat has expired, and replaces instances marked
// unhealthy.
//
// Cost when idle is one atomic load: with a known-zero group count a pass
// returns before issuing any store call at all. When groups do exist, a pass
// that changes nothing issues one scan per group and no EC2 call.
//
// What this file deliberately does NOT do is approximate the group shapes it
// cannot converge. A launch template, a mixed-instances policy or a
// launch-from-instance group is refused at CreateAutoScalingGroup (see
// validateGroupInput) rather than accepted and left reporting a desired
// capacity nothing acts on — the failure mode
// docs/plans/full-emulation-priority.md §2.1 exists to prevent.

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// reconcileInterval is how often the single background reconciler wakes when
// nothing has poked it. Handlers that change a group's shape poke the loop
// directly, so this interval controls how promptly a *time-driven* transition
// (a lifecycle-hook heartbeat expiring, a cooldown ending) is noticed, not how
// promptly a scaling request is acted on.
const reconcileInterval = 1 * time.Second

// ec2QueryVersion is the EC2 API version the reconciler speaks when launching
// and terminating instances.
const ec2QueryVersion = "2016-11-15"

// ─── Loop ─────────────────────────────────────────────────────────────────────

// runReconciler is the single background reconciliation loop. Service.Stop
// drains it.
func (s *Service) runReconciler() {
	defer s.wg.Done()
	defer s.ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-s.ticker.C:
			s.reconcileOnce(context.Background())
		case <-s.wake:
			s.reconcileOnce(context.Background())
		}
	}
}

// poke asks the loop to run a pass now rather than on its next tick. It never
// blocks: the channel is buffered to one, and a pending wake already covers a
// second request.
func (s *Service) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// invalidateGroupCount marks the cached group count unknown, so the next pass
// re-reads it. Called whenever a group is created or deleted.
func (s *Service) invalidateGroupCount() { s.groupCount.Store(-1) }

// reconcileOnce runs one reconciliation pass over every group, and reports
// whether it changed anything. Extracted from the loop so tests drive it
// directly instead of racing a clock.
func (s *Service) reconcileOnce(ctx context.Context) bool {
	observed := s.groupCount.Load()
	if observed == 0 {
		return false
	}

	groups, err := s.st.listGroups(ctx)
	if err != nil {
		return false
	}
	// Compare-and-swap rather than Store: a handler that created or deleted a
	// group while this scan was in flight has already set the count back to
	// -1, and overwriting that with a stale length would leave a new group
	// unreconciled forever once the count reached zero.
	s.groupCount.CompareAndSwap(observed, int64(len(groups)))
	if len(groups) == 0 {
		return false
	}

	changed := false
	for _, g := range groups {
		if s.reconcileGroup(ctx, g.AutoScalingGroupName) {
			changed = true
		}
	}
	// A pass that changed something usually leaves work for the next one — a
	// freshly launched instance is Pending and wants promoting. Re-poke rather
	// than waiting a full tick, so convergence is prompt without a second loop.
	if changed {
		s.poke()
	}
	return changed
}

// pendingHookEvent is a lifecycle-action notification to emit after the
// reconciler has released its lock.
type pendingHookEvent struct {
	instance   *ASGInstance
	transition string
	hookName   string
	token      string
}

// reconcileGroup converges one group. It never holds s.mu across an EC2 call:
// the lock only guards the read-modify-write of the group's own records.
func (s *Service) reconcileGroup(ctx context.Context, name string) bool {
	now := s.clk.Now().UTC()

	s.mu.Lock()
	g, ok := s.st.getGroup(ctx, name)
	if !ok {
		s.mu.Unlock()
		return false
	}
	instances, err := s.st.listInstances(ctx, name)
	if err != nil {
		s.mu.Unlock()
		return false
	}

	changed := false
	var events []pendingHookEvent
	var terminateNow []*ASGInstance

	// Resolve the group's hooks once per pass rather than once per instance:
	// hookFor is a store scan, and a group of fifty instances should not cost
	// fifty scans to discover the same two hooks.
	launchHook, hasLaunchHook := s.st.hookFor(ctx, name, transitionLaunching)
	terminateHook, hasTerminateHook := s.st.hookFor(ctx, name, transitionTerminating)

	// ── Advance the lifecycle state machine ──
	for _, inst := range instances {
		switch inst.LifecycleState {
		case lifecyclePending:
			if hook := launchHook; hasLaunchHook {
				s.parkOnHook(ctx, inst, hook, now)
				events = append(events, pendingHookEvent{
					instance: inst, transition: transitionLaunching,
					hookName: hook.LifecycleHookName, token: inst.LifecycleActionToken,
				})
			} else {
				s.promoteToInService(ctx, name, inst, now)
				s.emitInstanceEvent(ctx, g, inst, "EC2 Instance Launch Successful")
			}
			changed = true
		case lifecyclePendingWait:
			if s.hookExpired(inst, now) {
				if inst.HookDefaultResult == lifecycleResultAbandon {
					s.beginTermination(ctx, inst, now)
					terminateNow = append(terminateNow, inst)
				} else {
					s.clearHook(inst)
					s.promoteToInService(ctx, name, inst, now)
				}
				changed = true
			}
		case lifecycleTerminatingWt:
			if s.hookExpired(inst, now) {
				s.clearHook(inst)
				inst.LifecycleState = lifecycleTerminating
				_ = s.st.putInstance(ctx, inst)
				terminateNow = append(terminateNow, inst)
				changed = true
			}
		case lifecycleTerminating:
			terminateNow = append(terminateNow, inst)
		}
	}

	// ── Replace unhealthy instances ──
	// An instance marked Unhealthy is taken out of service; the capacity
	// arithmetic below then notices the shortfall and launches a replacement,
	// which is exactly the order real Auto Scaling does it in.
	for _, inst := range instances {
		if inst.HealthStatus != healthStatusUnhealthy {
			continue
		}
		if inst.LifecycleState != lifecycleInService && inst.LifecycleState != lifecyclePending {
			continue
		}
		if hook := terminateHook; hasTerminateHook {
			s.startTerminateActivity(ctx, g, inst, now, causeUnhealthy(now))
			s.parkOnHookForTermination(ctx, inst, hook, now)
			events = append(events, pendingHookEvent{
				instance: inst, transition: transitionTerminating,
				hookName: hook.LifecycleHookName, token: inst.LifecycleActionToken,
			})
		} else {
			s.startTerminateActivity(ctx, g, inst, now, causeUnhealthy(now))
			s.beginTermination(ctx, inst, now)
			terminateNow = append(terminateNow, inst)
		}
		changed = true
	}

	// ── Capacity arithmetic ──
	counted := 0
	for _, inst := range instances {
		if inst.counted() {
			counted++
		}
	}
	desired := clampCapacity(g.DesiredCapacity, g.MinSize, g.MaxSize)
	launchN := 0
	switch {
	case counted < desired:
		launchN = desired - counted
	case counted > desired:
		for _, victim := range scaleInVictims(instances, counted-desired) {
			if hook := terminateHook; hasTerminateHook {
				s.startTerminateActivity(ctx, g, victim, now, causeScaleIn(now, counted, desired))
				s.parkOnHookForTermination(ctx, victim, hook, now)
				events = append(events, pendingHookEvent{
					instance: victim, transition: transitionTerminating,
					hookName: hook.LifecycleHookName, token: victim.LifecycleActionToken,
				})
			} else {
				s.startTerminateActivity(ctx, g, victim, now, causeScaleIn(now, counted, desired))
				s.beginTermination(ctx, victim, now)
				terminateNow = append(terminateNow, victim)
			}
			changed = true
		}
	}
	launchCause := causeScaleOut(now, counted, desired)
	s.mu.Unlock()

	// ── Everything below runs without the lock ──
	for _, ev := range events {
		s.emitLifecycleAction(ctx, g, ev)
	}
	for _, inst := range terminateNow {
		s.finishTermination(ctx, g, inst)
		changed = true
	}
	for range launchN {
		if s.launchInstance(ctx, g, launchCause) {
			changed = true
		}
	}
	return changed
}

// clampCapacity holds a stored desired capacity inside the group's bounds.
// Validation refuses an out-of-range request at the API, so this only matters
// for a group whose min/max moved underneath a previously valid capacity —
// which is what real Auto Scaling does too.
func clampCapacity(desired, min, max int) int {
	if desired < min {
		return min
	}
	if max > 0 && desired > max {
		return max
	}
	if desired < 0 {
		return 0
	}
	return desired
}

// scaleInVictims picks n instances to remove. Real Auto Scaling's Default
// termination policy balances across Availability Zones and then prefers the
// oldest launch configuration; Overcast implements OldestInstance, which is
// the same answer for a single-AZ or uniform group and a documented
// simplification otherwise (docs/services/autoscaling.md).
func scaleInVictims(instances []*ASGInstance, n int) []*ASGInstance {
	candidates := make([]*ASGInstance, 0, len(instances))
	for _, inst := range instances {
		if !inst.counted() || inst.ProtectedFromScaleIn {
			continue
		}
		candidates = append(candidates, inst)
	}
	// Oldest first; instance id breaks ties so the choice is deterministic.
	for i := 1; i < len(candidates); i++ {
		for j := i; j > 0; j-- {
			a, b := candidates[j-1], candidates[j]
			if b.LaunchedAt.Before(a.LaunchedAt) ||
				(b.LaunchedAt.Equal(a.LaunchedAt) && b.InstanceId < a.InstanceId) {
				candidates[j-1], candidates[j] = b, a
				continue
			}
			break
		}
	}
	if n > len(candidates) {
		n = len(candidates)
	}
	return candidates[:n]
}

// ─── Lifecycle hook bookkeeping ───────────────────────────────────────────────

func (s *Service) hookExpired(inst *ASGInstance, now time.Time) bool {
	return !inst.HookDeadline.IsZero() && !now.Before(inst.HookDeadline)
}

func (s *Service) parkOnHook(ctx context.Context, inst *ASGInstance, hook *LifecycleHook, now time.Time) {
	inst.LifecycleState = lifecyclePendingWait
	s.applyHook(inst, hook, now)
	_ = s.st.putInstance(ctx, inst)
}

func (s *Service) parkOnHookForTermination(ctx context.Context, inst *ASGInstance, hook *LifecycleHook, now time.Time) {
	inst.LifecycleState = lifecycleTerminatingWt
	s.applyHook(inst, hook, now)
	_ = s.st.putInstance(ctx, inst)
}

func (s *Service) applyHook(inst *ASGInstance, hook *LifecycleHook, now time.Time) {
	timeout := hook.HeartbeatTimeout
	if timeout <= 0 {
		timeout = defaultHeartbeatTimeout
	}
	result := hook.DefaultResult
	if result == "" {
		result = lifecycleResultAbandon
	}
	inst.PendingHookName = hook.LifecycleHookName
	inst.HookDefaultResult = result
	inst.HookDeadline = now.Add(time.Duration(timeout) * time.Second)
	inst.LifecycleActionToken = newID()
}

func (s *Service) clearHook(inst *ASGInstance) {
	inst.PendingHookName = ""
	inst.HookDefaultResult = ""
	inst.HookDeadline = time.Time{}
	inst.LifecycleActionToken = ""
}

// beginTermination moves an instance into Terminating and persists it, so a
// concurrent pass cannot pick it a second time.
func (s *Service) beginTermination(ctx context.Context, inst *ASGInstance, _ time.Time) {
	s.clearHook(inst)
	inst.LifecycleState = lifecycleTerminating
	_ = s.st.putInstance(ctx, inst)
}

// ─── EC2 calls ────────────────────────────────────────────────────────────────

// launchInstance runs one EC2 instance for the group and records it. Returns
// whether anything changed.
func (s *Service) launchInstance(ctx context.Context, g *AutoScalingGroup, cause string) bool {
	params := url.Values{
		"Action":   {"RunInstances"},
		"Version":  {ec2QueryVersion},
		"MinCount": {"1"},
		"MaxCount": {"1"},
	}
	// A group launches from a template or from a launch configuration. With a
	// template, the parameters stay on the EC2 side: RunInstances resolves the
	// version and merges its data itself, so there is one implementation of
	// that merge rather than a second one here.
	instanceType := ""
	if g.LaunchTemplate != nil {
		if g.LaunchTemplate.LaunchTemplateId != "" {
			params.Set("LaunchTemplate.LaunchTemplateId", g.LaunchTemplate.LaunchTemplateId)
		} else {
			params.Set("LaunchTemplate.LaunchTemplateName", g.LaunchTemplate.LaunchTemplateName)
		}
		if g.LaunchTemplate.Version != "" {
			params.Set("LaunchTemplate.Version", g.LaunchTemplate.Version)
		}
	} else {
		lc, found := s.st.getLaunchConfig(ctx, g.LaunchConfigurationName)
		if !found {
			s.log.Warn("autoscaling: cannot launch: launch configuration is missing",
				zap.String("group", g.AutoScalingGroupName),
				zap.String("launch_configuration", g.LaunchConfigurationName))
			return false
		}
		instanceType = lc.InstanceType
		params.Set("ImageId", lc.ImageId)
		params.Set("InstanceType", lc.InstanceType)
		for i, sg := range lc.SecurityGroups {
			params.Set(fmt.Sprintf("SecurityGroupId.%d", i+1), sg)
		}
	}

	s.mu.Lock()
	existing, _ := s.st.listInstances(ctx, g.AutoScalingGroupName)
	s.mu.Unlock()
	subnet, az := nextPlacement(g, len(existing))
	if subnet != "" {
		params.Set("SubnetId", subnet)
	}
	if az != "" {
		params.Set("Placement.AvailabilityZone", az)
	}
	// Real Auto Scaling stamps every instance it launches with the group name
	// and with each group tag marked PropagateAtLaunch.
	params.Set("TagSpecification.1.ResourceType", "instance")
	params.Set("TagSpecification.1.Tag.1.Key", "aws:autoscaling:groupName")
	params.Set("TagSpecification.1.Tag.1.Value", g.AutoScalingGroupName)
	tagIdx := 2
	if tags, terr := s.st.listTagsForGroup(ctx, g.AutoScalingGroupName); terr == nil {
		for _, tag := range tags {
			if !tag.PropagateAtLaunch || tag.Key == "" {
				continue
			}
			params.Set(fmt.Sprintf("TagSpecification.1.Tag.%d.Key", tagIdx), tag.Key)
			params.Set(fmt.Sprintf("TagSpecification.1.Tag.%d.Value", tagIdx), tag.Value)
			tagIdx++
		}
	}

	body, err := s.ec2Call(ctx, params)
	if err != nil {
		s.log.Warn("autoscaling: RunInstances failed",
			zap.String("group", g.AutoScalingGroupName), zap.Error(err))
		s.recordFailedLaunch(ctx, g, cause, err)
		// Report "no change" deliberately. A failed launch leaves the group
		// short, so claiming a change would re-poke the loop, which would
		// retry at once, fail again, and spin — a busy loop writing an
		// activity per iteration. The next tick retries a second later, which
		// is the pace a persistently broken AMI deserves.
		return false
	}

	var resp struct {
		XMLName   xml.Name `xml:"RunInstancesResponse"`
		Instances []struct {
			InstanceID   string `xml:"instanceId"`
			InstanceType string `xml:"instanceType"`
			Placement    struct {
				AvailabilityZone string `xml:"availabilityZone"`
			} `xml:"placement"`
			SubnetID string `xml:"subnetId"`
		} `xml:"instancesSet>item"`
	}
	if err := xml.Unmarshal(body, &resp); err != nil || len(resp.Instances) == 0 {
		s.log.Warn("autoscaling: RunInstances returned no instance",
			zap.String("group", g.AutoScalingGroupName))
		return false
	}
	launched := resp.Instances[0]
	if launched.Placement.AvailabilityZone != "" {
		az = launched.Placement.AvailabilityZone
	}
	// With a launch template the instance type came out of the template, so
	// the answer is the one EC2 reports rather than one this side chose.
	if launched.InstanceType != "" {
		instanceType = launched.InstanceType
	}

	now := s.clk.Now().UTC()
	inst := &ASGInstance{
		InstanceId:              launched.InstanceID,
		AutoScalingGroupName:    g.AutoScalingGroupName,
		InstanceType:            instanceType,
		AvailabilityZone:        az,
		LifecycleState:          lifecyclePending,
		HealthStatus:            healthStatusHealthy,
		LaunchConfigurationName: g.LaunchConfigurationName,
		LaunchTemplate:          g.LaunchTemplate,
		ProtectedFromScaleIn:    g.NewInstancesProtectedFromScaleIn,
		LaunchedAt:              now,
	}

	s.mu.Lock()
	activity := s.recordActivity(ctx, g, &Activity{
		Description: "Launching a new EC2 instance: " + launched.InstanceID,
		Cause:       cause,
		StartTime:   now,
		StatusCode:  "PreInService",
		Progress:    50,
		Details:     activityDetails(launched.SubnetID, az),
	})
	inst.LaunchActivityId = activity
	_ = s.st.putInstance(ctx, inst)
	s.mu.Unlock()
	return true
}

// recordFailedLaunch writes the Failed activity real Auto Scaling writes when
// an instance cannot be launched, so the failure is visible rather than silent.
//
// A group whose AMI does not exist fails on every tick, so an identical
// failure is not recorded twice in a row: the activity log should say what is
// wrong, not fill up with one second's worth of the same sentence.
func (s *Service) recordFailedLaunch(ctx context.Context, g *AutoScalingGroup, cause string, cerr error) {
	now := s.clk.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if recent, err := s.st.listActivities(ctx, g.AutoScalingGroupName); err == nil && len(recent) > 0 {
		if last := recent[0]; last.StatusCode == "Failed" && last.StatusMessage == cerr.Error() {
			return
		}
	}
	s.recordActivity(ctx, g, &Activity{
		Description:   "Launching a new EC2 instance",
		Cause:         cause,
		StartTime:     now,
		EndTime:       now,
		StatusCode:    "Failed",
		StatusMessage: cerr.Error(),
		Progress:      100,
	})
}

// finishTermination terminates the EC2 instance and drops the record.
func (s *Service) finishTermination(ctx context.Context, g *AutoScalingGroup, inst *ASGInstance) {
	params := url.Values{
		"Action":       {"TerminateInstances"},
		"Version":      {ec2QueryVersion},
		"InstanceId.1": {inst.InstanceId},
	}
	if _, err := s.ec2Call(ctx, params); err != nil {
		// An instance EC2 no longer knows about is still ours to forget: the
		// alternative is a group that can never converge because a phantom
		// record keeps counting.
		s.log.Debug("autoscaling: TerminateInstances failed; dropping the record anyway",
			zap.String("group", g.AutoScalingGroupName),
			zap.String("instance", inst.InstanceId), zap.Error(err))
	}
	now := s.clk.Now().UTC()
	s.mu.Lock()
	s.completeActivity(ctx, g.AutoScalingGroupName, inst.TerminateActivityId, now)
	s.st.deleteInstance(ctx, g.AutoScalingGroupName, inst.InstanceId)
	s.mu.Unlock()
	s.emitInstanceEvent(ctx, g, inst, "EC2 Instance Terminate Successful")
}

// ec2Call dispatches a Query-protocol EC2 request through the emulator's own
// root router, so a launch takes exactly the path an SDK client's RunInstances
// would — a missing AMI produces EC2's own AWS error rather than a silent
// no-op.
func (s *Service) ec2Call(ctx context.Context, params url.Values) ([]byte, error) {
	body, status, err := s.ec2CallRaw(ctx, params)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// ec2CallRaw is ec2Call without the status check, for a caller that needs
// EC2's own error body rather than a message about it — validating a launch
// template, which reports EC2's wording inside Auto Scaling's ValidationError.
func (s *Service) ec2CallRaw(ctx context.Context, params url.Values) ([]byte, int, error) {
	if s.router == nil {
		return nil, 0, fmt.Errorf("autoscaling: no router configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(params.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Overcast-Region", s.region())
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	return rec.Body.Bytes(), rec.Code, nil
}

// resolveLaunchTemplate asks EC2 for the version a group's LaunchTemplate
// names, so a group is stored only when something can really be launched from
// it. It returns the pair of identifiers AWS echoes back regardless of which
// the caller supplied, and the version string verbatim: a group pinned to
// $Latest follows its template, one pinned to a number does not.
func (s *Service) resolveLaunchTemplate(ctx context.Context, spec launchTemplateSpec) (*ASGLaunchTemplate, *protocol.AWSError) {
	params := url.Values{
		"Action":                  {"DescribeLaunchTemplateVersions"},
		"Version":                 {ec2QueryVersion},
		"LaunchTemplateVersion.1": {launchTemplateVersionOrDefault(spec.Version)},
	}
	if spec.LaunchTemplateId != "" {
		params.Set("LaunchTemplateId", spec.LaunchTemplateId)
	}
	if spec.LaunchTemplateName != "" {
		params.Set("LaunchTemplateName", spec.LaunchTemplateName)
	}

	body, status, err := s.ec2CallRaw(ctx, params)
	if err != nil {
		return nil, &protocol.AWSError{
			Code:       "InternalFailure",
			Message:    "failed to resolve the launch template",
			HTTPStatus: http.StatusInternalServerError,
		}
	}
	if status >= 400 {
		return nil, asgValidationError("You must use a valid fully-formed launch template. %s", ec2ErrorMessage(body))
	}

	var resp struct {
		Versions []struct {
			LaunchTemplateId   string `xml:"launchTemplateId"`
			LaunchTemplateName string `xml:"launchTemplateName"`
		} `xml:"launchTemplateVersionSet>item"`
	}
	if err := xml.Unmarshal(body, &resp); err != nil || len(resp.Versions) == 0 {
		return nil, asgValidationError("You must use a valid fully-formed launch template. The specified launch template version does not exist.")
	}
	return &ASGLaunchTemplate{
		LaunchTemplateId:   resp.Versions[0].LaunchTemplateId,
		LaunchTemplateName: resp.Versions[0].LaunchTemplateName,
		Version:            spec.Version,
	}, nil
}

// launchTemplateVersionOrDefault is AWS's rule for a group that names a
// template without a version: the template's default version is used.
func launchTemplateVersionOrDefault(version string) string {
	if version == "" {
		return "$Default"
	}
	return version
}

// ec2ErrorMessage pulls EC2's own message out of a Query error body, so Auto
// Scaling's ValidationError can carry it the way AWS's does.
func ec2ErrorMessage(body []byte) string {
	var parsed struct {
		Message string `xml:"Errors>Error>Message"`
	}
	if err := xml.Unmarshal(body, &parsed); err == nil && parsed.Message != "" {
		return parsed.Message
	}
	return strings.TrimSpace(string(body))
}

// ─── Activities ───────────────────────────────────────────────────────────────

// recordActivity persists a new activity and returns its id. Callers hold s.mu.
func (s *Service) recordActivity(ctx context.Context, g *AutoScalingGroup, a *Activity) string {
	a.ActivityId = newID()
	a.AutoScalingGroupName = g.AutoScalingGroupName
	a.AutoScalingGroupARN = g.AutoScalingGroupARN
	if a.StartTime.IsZero() {
		a.StartTime = s.clk.Now().UTC()
	}
	if err := s.st.putActivity(ctx, a); err != nil {
		s.log.Debug("autoscaling: could not record scaling activity", zap.Error(err))
	}
	return a.ActivityId
}

// promoteToInService finishes a launch: the launch activity is marked
// Successful, then the instance is written InService. The order matters
// because the Describe handlers read the store without s.mu (#720). An
// instance written first could be seen InService by DescribeAutoScalingInstances
// while DescribeScalingActivities still reported its launch as PreInService.
// This way, anyone who sees InService also sees the launch completed.
// Termination is already in this order: finishTermination completes the
// activity before it deletes the instance. Callers hold s.mu.
func (s *Service) promoteToInService(ctx context.Context, group string, inst *ASGInstance, now time.Time) {
	s.completeActivity(ctx, group, inst.LaunchActivityId, now)
	inst.LifecycleState = lifecycleInService
	_ = s.st.putInstance(ctx, inst)
}

// completeActivity marks an in-flight activity Successful. Callers hold s.mu.
func (s *Service) completeActivity(ctx context.Context, group, activityID string, now time.Time) {
	if activityID == "" {
		return
	}
	activities, err := s.st.listActivities(ctx, group)
	if err != nil {
		return
	}
	for _, a := range activities {
		if a.ActivityId != activityID {
			continue
		}
		a.StatusCode = "Successful"
		a.Progress = 100
		a.EndTime = now
		if err := s.st.putActivity(ctx, a); err != nil {
			s.log.Debug("autoscaling: could not complete scaling activity", zap.Error(err))
		}
		return
	}
}

// startTerminateActivity records the InProgress activity for a termination and
// remembers its id on the instance. Callers hold s.mu.
func (s *Service) startTerminateActivity(ctx context.Context, g *AutoScalingGroup, inst *ASGInstance, now time.Time, cause string) {
	if inst.TerminateActivityId != "" {
		return
	}
	inst.TerminateActivityId = s.recordActivity(ctx, g, &Activity{
		Description: "Terminating EC2 instance: " + inst.InstanceId,
		Cause:       cause,
		StartTime:   now,
		StatusCode:  "InProgress",
		Progress:    50,
		Details:     activityDetails("", inst.AvailabilityZone),
	})
}

func activityDetails(subnet, az string) string {
	details := map[string]string{}
	if subnet != "" {
		details["Subnet ID"] = subnet
	}
	if az != "" {
		details["Availability Zone"] = az
	}
	if len(details) == 0 {
		return ""
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return ""
	}
	return string(raw)
}

// Cause sentences reproduce the wording real Auto Scaling puts on a scaling
// activity, so a caller that greps DescribeScalingActivities keeps working.

func causeScaleOut(now time.Time, from, to int) string {
	return fmt.Sprintf("At %s an instance was started in response to a difference between desired and actual capacity, increasing the capacity from %d to %d.",
		now.Format(awsTimeLayout), from, to)
}

func causeScaleIn(now time.Time, from, to int) string {
	return fmt.Sprintf("At %s an instance was taken out of service in response to a difference between desired and actual capacity, shrinking the capacity from %d to %d.",
		now.Format(awsTimeLayout), from, to)
}

func causeUnhealthy(now time.Time) string {
	return fmt.Sprintf("At %s an instance was taken out of service in response to a system health-check.",
		now.Format(awsTimeLayout))
}

func causeUserRequest(now time.Time, from, to, min, max int) string {
	return fmt.Sprintf("At %s a user request update of AutoScalingGroup constraints to min: %d, max: %d, desired: %d changing the desired capacity from %d to %d.",
		now.Format(awsTimeLayout), min, max, to, from, to)
}

func causePolicy(now time.Time, policy, alarm string, from, to int) string {
	if alarm == "" {
		return fmt.Sprintf("At %s a user request executed policy %s changing the desired capacity from %d to %d.",
			now.Format(awsTimeLayout), policy, from, to)
	}
	return fmt.Sprintf("At %s a monitor alarm %s in state ALARM triggered policy %s changing the desired capacity from %d to %d.",
		now.Format(awsTimeLayout), alarm, policy, from, to)
}

// ─── Placement ────────────────────────────────────────────────────────────────

// nextPlacement chooses where a group's next launch goes, and gives
// RunInstances exactly one of the two ways of saying it.
//
// A group with subnets round-robins across those and sends the subnet alone.
// EC2 places the instance in that subnet's own zone (launchAvailabilityZone in
// internal/services/ec2), so the zone and the subnet cannot contradict each
// other, because only one of the two is ever sent. Deriving the zone here as
// well and sending both would put a second copy of that rule on this side of
// the call, and getting it wrong by one index is exactly what #1840 was: the
// zones and the subnets were round-robinned independently, and nothing checked
// that subnet i was in zone i.
//
// A group with no VPCZoneIdentifier has only its zones to go on and
// round-robins those, so a multi-AZ group without subnets still spreads.
func nextPlacement(g *AutoScalingGroup, n int) (subnet, az string) {
	if subnets := subnetIDs(g.VPCZoneIdentifier); len(subnets) > 0 {
		return subnets[n%len(subnets)], ""
	}
	return "", nextAvailabilityZone(g, n)
}

// nextAvailabilityZone round-robins across the group's zones so a multi-AZ
// group spreads, as real Auto Scaling does.
func nextAvailabilityZone(g *AutoScalingGroup, n int) string {
	if len(g.AvailabilityZones) == 0 {
		return ""
	}
	return g.AvailabilityZones[n%len(g.AvailabilityZones)]
}

// subnetIDs splits a VPCZoneIdentifier into the subnets it names. AWS
// documents the field as "a comma-separated list of subnet IDs"; the trimming
// is for the spacing hand-written templates and CLI invocations put around the
// commas.
func subnetIDs(vpcZoneIdentifier string) []string {
	if vpcZoneIdentifier == "" {
		return nil
	}
	parts := strings.Split(vpcZoneIdentifier, ",")
	ids := parts[:0]
	for _, id := range parts {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// subnetZones asks EC2 for the zone of every subnet a VPCZoneIdentifier names
// and returns them in the order the subnets were listed, without repeats — the
// zone list AWS derives for a group that gives subnets.
//
// ok is false when EC2 could not resolve every one of them. A group naming a
// subnet the store does not hold is then left exactly as it was rather than
// refused or half-derived: an unknown subnet keeps the permissive fallback
// RunInstances gives it (launchAvailabilityZone, #1839) instead of becoming a
// new way for a group to fail.
func (s *Service) subnetZones(ctx context.Context, vpcZoneIdentifier string) ([]string, bool) {
	ids := subnetIDs(vpcZoneIdentifier)
	if len(ids) == 0 {
		return nil, false
	}
	params := url.Values{
		"Action":  {"DescribeSubnets"},
		"Version": {ec2QueryVersion},
	}
	for i, id := range ids {
		params.Set(fmt.Sprintf("SubnetId.%d", i+1), id)
	}
	body, status, err := s.ec2CallRaw(ctx, params)
	if err != nil || status >= 400 {
		return nil, false
	}
	var resp struct {
		Subnets []struct {
			SubnetID         string `xml:"subnetId"`
			AvailabilityZone string `xml:"availabilityZone"`
		} `xml:"subnetSet>item"`
	}
	if err := xml.Unmarshal(body, &resp); err != nil {
		return nil, false
	}
	zoneOf := make(map[string]string, len(resp.Subnets))
	for _, sn := range resp.Subnets {
		zoneOf[sn.SubnetID] = sn.AvailabilityZone
	}
	zones := make([]string, 0, len(ids))
	for _, id := range ids {
		zone := zoneOf[id]
		if zone == "" {
			return nil, false
		}
		if !slices.Contains(zones, zone) {
			zones = append(zones, zone)
		}
	}
	return zones, true
}

// ─── EventBridge notifications ────────────────────────────────────────────────

// emitLifecycleAction publishes the lifecycle-action event real Auto Scaling
// puts on the default event bus when an instance parks on a hook. It goes
// through the emulator's own PutEvents so any rule matching it is evaluated
// exactly as it would be for an SDK-published event.
func (s *Service) emitLifecycleAction(ctx context.Context, g *AutoScalingGroup, ev pendingHookEvent) {
	detailType := "EC2 Instance-launch Lifecycle Action"
	if ev.transition == transitionTerminating {
		detailType = "EC2 Instance-terminate Lifecycle Action"
	}
	s.putEvent(ctx, detailType, map[string]any{
		"LifecycleActionToken": ev.token,
		"AutoScalingGroupName": g.AutoScalingGroupName,
		"LifecycleHookName":    ev.hookName,
		"EC2InstanceId":        ev.instance.InstanceId,
		"LifecycleTransition":  ev.transition,
	}, g.AutoScalingGroupARN)
}

// emitInstanceEvent publishes the launch/terminate success events real Auto
// Scaling emits.
func (s *Service) emitInstanceEvent(ctx context.Context, g *AutoScalingGroup, inst *ASGInstance, detailType string) {
	s.putEvent(ctx, detailType, map[string]any{
		"StatusCode":           "InProgress",
		"AutoScalingGroupName": g.AutoScalingGroupName,
		"EC2InstanceId":        inst.InstanceId,
		"Cause":                "",
	}, g.AutoScalingGroupARN)
}

// putEvent posts one aws.autoscaling event through the real PutEvents.
func (s *Service) putEvent(ctx context.Context, detailType string, detail map[string]any, resource string) {
	if s.router == nil {
		return
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return
	}
	body, err := json.Marshal(map[string]any{
		"Entries": []any{
			map[string]any{
				"Source":     "aws.autoscaling",
				"DetailType": detailType,
				"Detail":     string(detailJSON),
				"Resources":  []string{resource},
				"Time":       s.clk.Now().UTC().Format(time.RFC3339),
			},
		},
	})
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSEvents.PutEvents")
	req.Header.Set("X-Overcast-Region", s.region())
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code >= 400 {
		s.log.Debug("autoscaling: lifecycle event was not published",
			zap.String("detail_type", detailType), zap.Int("status", rec.Code))
	}
}

// ─── Errors ───────────────────────────────────────────────────────────────────

func asgValidationError(format string, args ...any) *protocol.AWSError {
	return &protocol.AWSError{
		Code: "ValidationError", Message: fmt.Sprintf(format, args...),
		HTTPStatus: http.StatusBadRequest,
	}
}

func asgUnsupported(format string, args ...any) *protocol.AWSError {
	return &protocol.AWSError{
		Code: "NotImplemented", Message: fmt.Sprintf(format, args...),
		HTTPStatus: http.StatusNotImplemented,
	}
}
