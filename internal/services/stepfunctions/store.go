package stepfunctions

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const (
	storeNS     = "stepfunctions"
	smPrefix    = "sm:"
	execPrefix  = "exec:"
	histPrefix  = "hist:"
	verPrefix   = "ver:"
	aliasPrefix = "alias:"
)

// StateMachine represents a Step Functions state machine.
func (sm *StateMachine) GetTags() map[string]string  { return sm.Tags }
func (sm *StateMachine) SetTags(t map[string]string) { sm.Tags = t }

type StateMachine struct {
	Name       string            `json:"Name"`
	ARN        string            `json:"ARN"`
	Definition string            `json:"Definition"`
	RoleArn    string            `json:"RoleArn"`
	Type       string            `json:"Type"` // "STANDARD" or "EXPRESS"
	Status     string            `json:"Status"`
	CreatedAt  time.Time         `json:"CreatedAt"`
	Tags       map[string]string `json:"Tags,omitempty"`
	// LoggingConfiguration and TracingConfiguration are stored as the raw
	// shape CreateStateMachine/UpdateStateMachine were given (AWS's
	// LoggingConfiguration/TracingConfiguration structures) and echoed back
	// verbatim by DescribeStateMachine. Overcast does not act on either —
	// no CloudWatch Logs delivery, no X-Ray spans — it only stops dropping
	// them, matching the "accept and echo" treatment other cosmetic,
	// unimplemented-behavior config blocks get elsewhere in the emulator.
	LoggingConfiguration map[string]any `json:"LoggingConfiguration,omitempty"`
	TracingConfiguration map[string]any `json:"TracingConfiguration,omitempty"`
	// RevisionID identifies the current revision of the definition, role,
	// logging and tracing configuration. Empty for a state machine that has
	// never been updated — AWS reports no revisionId for the initial
	// revision, and PublishStateMachineVersion matches it as "INITIAL".
	RevisionID string `json:"RevisionId,omitempty"`
	// LastVersion is the highest version number ever published. Version
	// numbers are never reused, even after DeleteStateMachineVersion, so the
	// counter lives here rather than being derived from the versions left.
	LastVersion int `json:"LastVersion,omitempty"`
}

// StateMachineVersion is an immutable snapshot of a state machine revision,
// published by PublishStateMachineVersion (or CreateStateMachine /
// UpdateStateMachine with publish=true). Its ARN is the state machine ARN
// qualified with the version number.
type StateMachineVersion struct {
	StateMachineName     string         `json:"StateMachineName"`
	ARN                  string         `json:"ARN"`
	Version              int            `json:"Version"`
	Definition           string         `json:"Definition"`
	RoleArn              string         `json:"RoleArn"`
	Type                 string         `json:"Type"`
	Description          string         `json:"Description,omitempty"`
	RevisionID           string         `json:"RevisionId,omitempty"`
	LoggingConfiguration map[string]any `json:"LoggingConfiguration,omitempty"`
	TracingConfiguration map[string]any `json:"TracingConfiguration,omitempty"`
	CreatedAt            time.Time      `json:"CreatedAt"`
}

// StateMachineAlias routes executions to one or two versions of a state
// machine by weight. Its ARN is the state machine ARN qualified with the
// alias name.
type StateMachineAlias struct {
	StateMachineName     string             `json:"StateMachineName"`
	Name                 string             `json:"Name"`
	ARN                  string             `json:"ARN"`
	Description          string             `json:"Description,omitempty"`
	RoutingConfiguration []aliasRouteRecord `json:"RoutingConfiguration"`
	CreatedAt            time.Time          `json:"CreatedAt"`
	UpdatedAt            time.Time          `json:"UpdatedAt"`
}

// aliasRouteRecord is one persisted RoutingConfigurationListItem.
type aliasRouteRecord struct {
	StateMachineVersionArn string `json:"StateMachineVersionArn"`
	Weight                 int    `json:"Weight"`
}

// Execution represents a Step Functions execution.
type Execution struct {
	ExecutionArn    string     `json:"ExecutionArn"`
	StateMachineArn string     `json:"StateMachineArn"`
	Name            string     `json:"Name"`
	Input           string     `json:"Input"`
	Output          string     `json:"Output,omitempty"`
	Status          string     `json:"Status"`
	StartDate       time.Time  `json:"StartDate"`
	StopDate        *time.Time `json:"StopDate,omitempty"`
	// Error and Cause carry the AWS-shaped failure reason for a FAILED,
	// TIMED_OUT or ABORTED execution — including the loud "not supported"
	// failures Overcast raises for ASL features it does not interpret.
	Error string `json:"Error,omitempty"`
	Cause string `json:"Cause,omitempty"`

	// Redrive bookkeeping. RedriveState/RedriveInput are the top-level state
	// the last run failed in and the raw input it was entered with; they are
	// what RedriveExecution resumes from.
	RedriveCount int        `json:"RedriveCount,omitempty"`
	RedriveDate  *time.Time `json:"RedriveDate,omitempty"`
	RedriveState string     `json:"RedriveState,omitempty"`
	RedriveInput string     `json:"RedriveInput,omitempty"`
	// RedriveVariables is the variables in scope when that state was entered.
	RedriveVariables string `json:"RedriveVariables,omitempty"`
	// MapRunArn is set on a child execution a distributed Map started.
	MapRunArn string `json:"MapRunArn,omitempty"`
	// StateMachineVersionArn and StateMachineAliasArn record the qualified
	// ARN an execution was started through: the version that ran (directly,
	// or the one an alias routed to) and the alias, if any. Both are empty
	// for an execution started against the unqualified state machine ARN.
	StateMachineVersionArn string `json:"StateMachineVersionArn,omitempty"`
	StateMachineAliasArn   string `json:"StateMachineAliasArn,omitempty"`
}

// Store wraps state.Store with Step Functions-specific helpers.
type Store struct {
	s             state.Store
	defaultRegion string

	// eventBridge is the EventBridge bus publisher wired by
	// Service.InitEventBridge (eventbridge.go), once the EventBridge service
	// itself exists. Nil in most unit tests, which makes
	// notifyExecutionStatusChange a no-op — see its doc comment.
	eventBridge events.BusPublisher
}

func newStore(s state.Store, defaultRegion string) *Store {
	return &Store{s: s, defaultRegion: defaultRegion}
}

// region extracts the per-request region from context, falling back to the default.
func (st *Store) region(ctx context.Context) string {
	return middleware.RegionFromContext(ctx, st.defaultRegion)
}

// GetStateMachine retrieves a state machine by name. Returns nil, nil if not found.
func (st *Store) GetStateMachine(ctx context.Context, name string) (*StateMachine, error) {
	raw, found, err := st.s.Get(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), smPrefix+name))
	if err != nil {
		return nil, fmt.Errorf("stepfunctions: get sm %q: %w", name, err)
	}
	if !found {
		return nil, nil
	}
	var sm StateMachine
	if err := json.Unmarshal([]byte(raw), &sm); err != nil {
		return nil, fmt.Errorf("stepfunctions: unmarshal sm %q: %w", name, err)
	}
	return &sm, nil
}

// PutStateMachine saves a state machine record.
func (st *Store) PutStateMachine(ctx context.Context, sm *StateMachine) error {
	raw, err := json.Marshal(sm)
	if err != nil {
		return fmt.Errorf("stepfunctions: marshal sm %q: %w", sm.Name, err)
	}
	return st.s.Set(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), smPrefix+sm.Name), string(raw))
}

// DeleteStateMachine removes a state machine by name.
func (st *Store) DeleteStateMachine(ctx context.Context, name string) error {
	return st.s.Delete(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), smPrefix+name))
}

// ListStateMachines returns all state machines.
func (st *Store) ListStateMachines(ctx context.Context) ([]*StateMachine, error) {
	pairs, err := st.s.Scan(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), smPrefix))
	if err != nil {
		return nil, fmt.Errorf("stepfunctions: scan sms: %w", err)
	}
	sms := make([]*StateMachine, 0, len(pairs))
	for _, p := range pairs {
		var sm StateMachine
		if err := json.Unmarshal([]byte(p.Value), &sm); err != nil {
			return nil, fmt.Errorf("stepfunctions: unmarshal sm: %w", err)
		}
		sms = append(sms, &sm)
	}
	return sms, nil
}

// PutExecution saves an execution record.
//
// Every execution status write — StartExecution's initial RUNNING record,
// completeExecution's terminal write (SUCCEEDED/FAILED/TIMED_OUT/ABORTED),
// the panic-recovery FAILED write, and StopExecution's direct ABORTED write —
// funnels through here, so it is the single place to diff the prior status
// against the new one and emit the EventBridge notification. See
// notifyExecutionStatusChange in eventbridge.go, mirroring
// ec2Store.putInstance / ecsStore.putTask for the same purpose.
func (st *Store) PutExecution(ctx context.Context, exec *Execution) error {
	// Read the prior record before overwriting it so the write below can tell
	// whether the execution's status actually changed. A lookup failure
	// (including "not found", the very first write for this execution ARN)
	// just means prev is nil; it is not fatal to the put itself.
	prev, _ := st.GetExecution(ctx, exec.ExecutionArn)

	raw, err := json.Marshal(exec)
	if err != nil {
		return fmt.Errorf("stepfunctions: marshal exec %q: %w", exec.ExecutionArn, err)
	}
	if err := st.s.Set(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), execPrefix+exec.ExecutionArn), string(raw)); err != nil {
		return err
	}
	st.notifyExecutionStatusChange(ctx, prev, exec)
	return nil
}

// GetExecution retrieves one execution by ARN. Returns nil, nil if not found.
func (st *Store) GetExecution(ctx context.Context, arn string) (*Execution, error) {
	raw, found, err := st.s.Get(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), execPrefix+arn))
	if err != nil {
		return nil, fmt.Errorf("stepfunctions: get exec %q: %w", arn, err)
	}
	if !found {
		return nil, nil
	}
	var exec Execution
	if err := json.Unmarshal([]byte(raw), &exec); err != nil {
		return nil, fmt.Errorf("stepfunctions: unmarshal exec %q: %w", arn, err)
	}
	return &exec, nil
}

// ListExecutions returns every execution of one state machine, newest first.
//
// Execution ARNs embed the state machine name, so the store key prefix scopes
// the scan to that machine rather than walking every execution in the region.
// A record that cannot be decoded is skipped rather than failing the whole
// list — one corrupt row must not take out the page.
func (st *Store) ListExecutions(ctx context.Context, smARN string) ([]*Execution, error) {
	return st.ListExecutionsWithPrefix(ctx, executionARNPrefix(smARN))
}

// ListExecutionsWithPrefix returns every execution whose ARN starts with
// arnPrefix, newest first.
func (st *Store) ListExecutionsWithPrefix(ctx context.Context, arnPrefix string) ([]*Execution, error) {
	prefix := execPrefix + arnPrefix
	pairs, err := st.s.Scan(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), prefix))
	if err != nil {
		return nil, fmt.Errorf("stepfunctions: scan executions: %w", err)
	}
	execs := make([]*Execution, 0, len(pairs))
	for _, p := range pairs {
		var exec Execution
		if json.Unmarshal([]byte(p.Value), &exec) != nil {
			continue
		}
		execs = append(execs, &exec)
	}
	sort.SliceStable(execs, func(i, j int) bool { return execs[i].StartDate.After(execs[j].StartDate) })
	return execs, nil
}

// PutHistory saves an execution's history in one write. The interpreter
// accumulates events in memory and calls this once, so a long execution costs
// a single store write rather than one per state transition.
func (st *Store) PutHistory(ctx context.Context, execARN string, events []HistoryEvent) error {
	raw, err := json.Marshal(events)
	if err != nil {
		return fmt.Errorf("stepfunctions: marshal history %q: %w", execARN, err)
	}
	return st.s.Set(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), histPrefix+execARN), string(raw))
}

// GetHistory returns an execution's recorded history events, oldest first.
func (st *Store) GetHistory(ctx context.Context, execARN string) ([]HistoryEvent, error) {
	raw, found, err := st.s.Get(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), histPrefix+execARN))
	if err != nil {
		return nil, fmt.Errorf("stepfunctions: get history %q: %w", execARN, err)
	}
	if !found {
		return nil, nil
	}
	var events []HistoryEvent
	if err := json.Unmarshal([]byte(raw), &events); err != nil {
		return nil, fmt.Errorf("stepfunctions: unmarshal history %q: %w", execARN, err)
	}
	return events, nil
}

// versionKey and aliasKey scope a state machine's versions and aliases under
// its name, so one prefix scan lists them and DeleteStateMachine can sweep
// them. State machine names cannot contain ':', so the prefixes of two
// different state machines never overlap.
func versionKey(smName string, version int) string {
	return verPrefix + smName + ":" + strconv.Itoa(version)
}

func aliasKey(smName, aliasName string) string {
	return aliasPrefix + smName + ":" + aliasName
}

// PutVersion saves a state machine version.
func (st *Store) PutVersion(ctx context.Context, v *StateMachineVersion) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("stepfunctions: marshal version %q: %w", v.ARN, err)
	}
	return st.s.Set(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), versionKey(v.StateMachineName, v.Version)), string(raw))
}

// GetVersion retrieves one version. Returns nil, nil if not found.
func (st *Store) GetVersion(ctx context.Context, smName string, version int) (*StateMachineVersion, error) {
	raw, found, err := st.s.Get(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), versionKey(smName, version)))
	if err != nil {
		return nil, fmt.Errorf("stepfunctions: get version %s:%d: %w", smName, version, err)
	}
	if !found {
		return nil, nil
	}
	var v StateMachineVersion
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, fmt.Errorf("stepfunctions: unmarshal version %s:%d: %w", smName, version, err)
	}
	return &v, nil
}

// DeleteVersion removes one version.
func (st *Store) DeleteVersion(ctx context.Context, smName string, version int) error {
	return st.s.Delete(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), versionKey(smName, version)))
}

// ListVersions returns every version of a state machine, newest first. A
// record that cannot be decoded is skipped rather than failing the list.
func (st *Store) ListVersions(ctx context.Context, smName string) ([]*StateMachineVersion, error) {
	pairs, err := st.s.Scan(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), verPrefix+smName+":"))
	if err != nil {
		return nil, fmt.Errorf("stepfunctions: scan versions of %q: %w", smName, err)
	}
	versions := make([]*StateMachineVersion, 0, len(pairs))
	for _, p := range pairs {
		var v StateMachineVersion
		if json.Unmarshal([]byte(p.Value), &v) != nil {
			continue
		}
		versions = append(versions, &v)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Version > versions[j].Version })
	return versions, nil
}

// PutAlias saves a state machine alias.
func (st *Store) PutAlias(ctx context.Context, a *StateMachineAlias) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("stepfunctions: marshal alias %q: %w", a.ARN, err)
	}
	return st.s.Set(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), aliasKey(a.StateMachineName, a.Name)), string(raw))
}

// GetAlias retrieves one alias. Returns nil, nil if not found.
func (st *Store) GetAlias(ctx context.Context, smName, aliasName string) (*StateMachineAlias, error) {
	raw, found, err := st.s.Get(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), aliasKey(smName, aliasName)))
	if err != nil {
		return nil, fmt.Errorf("stepfunctions: get alias %s:%s: %w", smName, aliasName, err)
	}
	if !found {
		return nil, nil
	}
	var a StateMachineAlias
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("stepfunctions: unmarshal alias %s:%s: %w", smName, aliasName, err)
	}
	return &a, nil
}

// DeleteAlias removes one alias.
func (st *Store) DeleteAlias(ctx context.Context, smName, aliasName string) error {
	return st.s.Delete(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), aliasKey(smName, aliasName)))
}

// ListAliases returns every alias of a state machine, most recently created
// first (ties broken by name, descending, so the order is stable). A record
// that cannot be decoded is skipped rather than failing the list.
func (st *Store) ListAliases(ctx context.Context, smName string) ([]*StateMachineAlias, error) {
	pairs, err := st.s.Scan(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), aliasPrefix+smName+":"))
	if err != nil {
		return nil, fmt.Errorf("stepfunctions: scan aliases of %q: %w", smName, err)
	}
	aliases := make([]*StateMachineAlias, 0, len(pairs))
	for _, p := range pairs {
		var a StateMachineAlias
		if json.Unmarshal([]byte(p.Value), &a) != nil {
			continue
		}
		aliases = append(aliases, &a)
	}
	sort.Slice(aliases, func(i, j int) bool {
		if !aliases[i].CreatedAt.Equal(aliases[j].CreatedAt) {
			return aliases[i].CreatedAt.After(aliases[j].CreatedAt)
		}
		return aliases[i].Name > aliases[j].Name
	})
	return aliases, nil
}

// executionARNPrefix turns a state machine ARN into the prefix every one of
// its execution ARNs starts with:
//
//	arn:aws:states:R:A:stateMachine:Name → arn:aws:states:R:A:execution:Name:
func executionARNPrefix(smARN string) string {
	name := extractSMName(smARN)
	parts := strings.SplitN(smARN, ":", 7)
	if len(parts) < 6 {
		return "arn:aws:states:::execution:" + name + ":"
	}
	return strings.Join(parts[:5], ":") + ":execution:" + name + ":"
}
