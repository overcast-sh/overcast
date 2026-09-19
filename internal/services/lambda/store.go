package lambda

// store.go — Lambda state access helpers.
//
// All Lambda state flows through lambdaStore, never through direct state.Store calls
// in handlers or the invoker. JSON serialisation lives here.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const (
	// nsFunctions is the state store namespace for Lambda function definitions.
	// Keys are function names.
	nsFunctions = "lambda:functions"

	// nsInstance holds this instance's sweep-domain identity, stamped into
	// docker.LabelInstance on every runtime container. See
	// serviceutil.InstanceDomain.
	nsInstance = "lambda:instance"

	// nsFunctionCode holds each function's deployment package (base64 of the
	// zip), split from the function record: the record is read on every invoke,
	// and embedding the package made each of those reads decode the whole zip.
	// Keys are "{functionName}/{codeHash}" — generation-addressed, so a write
	// never overwrites the package the currently stored record points at.
	// Written by putFunction, read by loadFunctionCode — only the paths that
	// need the actual bytes (cold start, source viewing/patching) ever touch
	// it. Packages persisted before generation addressing sit under the bare
	// function name; loadFunctionCode still reads those, and the next code
	// write supersedes and removes them.
	nsFunctionCode = "lambda:function-code"

	// nsFunctionPolicies stores resource policies separately from function
	// records so policy mutation cannot rewrite or corrupt function state.
	// Keys are "{functionName}|{qualifier}".
	nsFunctionPolicies = "lambda:function-policies"

	// nsInvocations is the state store namespace for invocation records.
	// Keys are "{functionName}:{timestamp_ns}" so each invocation is distinct.
	nsInvocations = "lambda:invocations"

	// nsTestEvents is the state store namespace for saved test events.
	// Keys are "{functionName}:{eventName}".
	nsTestEvents = "lambda:test-events"
)

// lambdaStore wraps state.Store with Lambda-specific helpers.
type lambdaStore struct {
	mu            sync.Mutex
	store         state.Store
	defaultRegion string
	clk           clock.Clock
}

func newLambdaStore(store state.Store, defaultRegion string, clk clock.Clock) *lambdaStore {
	return &lambdaStore{store: store, defaultRegion: defaultRegion, clk: clk}
}

// region extracts the per-request region from context, falling back to the default.
func (s *lambdaStore) region(ctx context.Context) string {
	return middleware.RegionFromContext(ctx, s.defaultRegion)
}

// getFunction retrieves a function by name. Returns nil, nil when not found.
func (s *lambdaStore) getFunction(ctx context.Context, name string) (*Function, *protocol.AWSError) {
	return s.getFunctionIn(ctx, s.region(ctx), name)
}

// getFunctionIn reads a function from an explicit region rather than the
// request's. Package materialization and cleanup need it: a cold start can run
// on a bare context and must resolve the record in the partition the function
// was written to.
func (s *lambdaStore) getFunctionIn(ctx context.Context, region, name string) (*Function, *protocol.AWSError) {
	raw, found, err := s.store.Get(ctx, nsFunctions, serviceutil.RegionKey(region, name))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, nil
	}
	var fn Function
	if err := json.Unmarshal([]byte(raw), &fn); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, fmt.Errorf("lambda: decode function %q: %w", name, err))
	}
	return &fn, nil
}

// functionCodeKey addresses a deployment package by the generation that owns
// it. Function names cannot contain "/", so the separator makes a function's
// packages a clean Scan prefix.
func functionCodeKey(name, generation string) string { return name + "/" + generation }

// functionCodeGeneration names the package a record references: the hex
// SHA-256 setCode stored alongside it. "" means the record addresses no
// generation — a container-image function with no package at all, or a record
// persisted before the hash field existed, whose package lives under the
// pre-generation key.
func functionCodeGeneration(fn *Function) string {
	if fn.CodeSize == 0 && len(fn.CodeZip) == 0 {
		return ""
	}
	if fn.CodeHash != "" {
		return fn.CodeHash
	}
	return codeHashOf(fn.CodeZip)
}

// putFunction stores a function definition. The deployment package is stored
// under its own key (nsFunctionCode) and stripped from the record, so reads
// that only need configuration — every invoke — never decode the package.
// The package key is written only when fn actually carries bytes; a put of a
// record whose code was never loaded (the usual state-transition update)
// leaves the stored package untouched.
//
// The package is generation-addressed and the record write is the single
// visibility point. Bytes land under a key nothing references yet, so a
// failure between the two writes leaves the previous generation whole: the
// stored record still names the package it was written with, and readers
// cannot observe half of the new deployment. What the failed attempt wrote is
// taken back out, since no record will ever point at it.
func (s *lambdaStore) putFunction(ctx context.Context, fn *Function) *protocol.AWSError {
	record := *fn
	record.CodeZip = nil
	raw, err := json.Marshal(&record)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	region := s.region(ctx)
	key := serviceutil.RegionKey(region, fn.Name)
	generation := ""
	if len(fn.CodeZip) > 0 {
		generation = functionCodeGeneration(fn)
		codeKey := serviceutil.RegionKey(region, functionCodeKey(fn.Name, generation))
		if err := s.store.Set(ctx, nsFunctionCode, codeKey, base64.StdEncoding.EncodeToString(fn.CodeZip)); err != nil {
			return protocol.Wrap(protocol.ErrInternalError, err)
		}
	}
	if err := s.store.Set(ctx, nsFunctions, key, string(raw)); err != nil {
		s.discardUnreferencedPackage(ctx, region, fn.Name, generation)
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if generation != "" {
		// The new generation is visible; every older one is unreachable.
		_ = s.pruneFunctionPackages(ctx, region, fn.Name, generation)
	}
	return nil
}

// discardUnreferencedPackage removes the package a failed record write left
// behind. The committed record decides: a put that rewrote the bytes the
// stored record already names must keep them, or the surviving generation
// would lose its package. Best effort — a package left resident here is
// unreferenced, and the next successful code write collects it.
func (s *lambdaStore) discardUnreferencedPackage(ctx context.Context, region, name, generation string) {
	if generation == "" {
		return
	}
	current, aerr := s.getFunctionIn(ctx, region, name)
	if aerr != nil || (current != nil && functionCodeGeneration(current) == generation) {
		return
	}
	_ = s.store.Delete(ctx, nsFunctionCode, serviceutil.RegionKey(region, functionCodeKey(name, generation)))
}

// pruneFunctionPackages deletes every stored package for a function except the
// generation named by keep, along with the pre-generation key that any
// generation supersedes. keep == "" removes all of them, which is what
// DeleteFunction wants.
//
// It only ever removes generations the stored record does not name, so a
// concurrent reader that already resolved the current generation still finds
// its bytes; one holding an older snapshot falls back to the current record
// (see loadFunctionCode) instead of cold starting with no package.
func (s *lambdaStore) pruneFunctionPackages(ctx context.Context, region, name, keep string) error {
	keepKey := ""
	if keep != "" {
		keepKey = serviceutil.RegionKey(region, functionCodeKey(name, keep))
	}
	kvs, err := s.store.Scan(ctx, nsFunctionCode, serviceutil.RegionKey(region, name+"/"))
	if err != nil {
		return err
	}
	for _, kv := range kvs {
		if kv.Key == keepKey {
			continue
		}
		if err := s.store.Delete(ctx, nsFunctionCode, kv.Key); err != nil {
			return err
		}
	}
	return s.store.Delete(ctx, nsFunctionCode, serviceutil.RegionKey(region, name))
}

// createFunction commits a new function only while its name is still absent.
// CreateFunction performs an early read for fast conflict responses, but all
// validation and external lookups happen after it; this locked recheck is the
// authority that prevents two concurrent creates from both succeeding.
func (s *lambdaStore) createFunction(ctx context.Context, fn *Function) (bool, *protocol.AWSError) {
	return s.createFunctionPublishing(ctx, fn, nil)
}

// createFunctionPublishing is createFunction for a Publish=true create:
// initial is the version 1 snapshot, committed in the same critical section
// as the function, with its number allocated here. No reader sees the
// function without its version, no racing create on the same name can
// publish on top of a function it did not create, and a snapshot that cannot
// be stored takes the function record down with it, so the create persists
// nothing — as every other failed create does. A nil initial is a plain
// createFunction.
func (s *lambdaStore) createFunctionPublishing(ctx context.Context, fn *Function, initial *FunctionVersion) (bool, *protocol.AWSError) {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, aerr := s.getFunction(ctx, fn.Name)
	if aerr != nil || existing != nil {
		return false, aerr
	}
	if aerr := s.putFunction(ctx, fn); aerr != nil {
		return false, aerr
	}
	if initial == nil {
		return true, nil
	}
	if aerr := s.publishVersionLocked(ctx, initial); aerr != nil {
		// Best effort, like discardUnreferencedPackage: the record is what a
		// reader would find, so it goes; a package left behind is
		// unreferenced and the next successful write collects it.
		region := s.region(ctx)
		_ = s.pruneFunctionPackages(ctx, region, fn.Name, "")
		_ = s.store.Delete(ctx, nsFunctions, serviceutil.RegionKey(region, fn.Name))
		return false, aerr
	}
	return true, nil
}

// mutateFunction serializes a function read-modify-write cycle. Callers do
// any slow external work before entering the callback, then re-check the
// current function state inside it. Every update to an existing function
// record uses this path so a stale writer cannot restore superseded code,
// source metadata, configuration, or lifecycle state.
func (s *lambdaStore) mutateFunction(ctx context.Context, name string, mutate func(*Function) (bool, *protocol.AWSError)) (*Function, bool, *protocol.AWSError) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fn, aerr := s.getFunction(ctx, name)
	if aerr != nil || fn == nil {
		return fn, false, aerr
	}
	changed, aerr := mutate(fn)
	if aerr != nil || !changed {
		return fn, false, aerr
	}
	if aerr := s.putFunction(ctx, fn); aerr != nil {
		return nil, false, aerr
	}
	return fn, true, nil
}

// loadFunctionCode populates fn.CodeZip from the stored package. Call it on
// the paths that need the actual bytes — cold start, source viewing and
// patching — never on the invoke path, which reads CodeHash alone. A record
// that already carries bytes (fresh setCode, or a legacy record with the
// package embedded) is left as is.
func (s *lambdaStore) loadFunctionCode(ctx context.Context, fn *Function) *protocol.AWSError {
	if fn == nil || len(fn.CodeZip) > 0 {
		return nil
	}
	// The function's own region, not the caller's: cold starts triggered off a
	// request (provisioned-concurrency replenish, ESM delivery) run on a bare
	// context, and the package must still resolve to the partition the
	// function was written to.
	region := regionFromFunctionARN(fn.ARN)
	if region == "" {
		region = s.region(ctx)
	}
	generation := functionCodeGeneration(fn)
	zip, found, aerr := s.readFunctionPackage(ctx, region, fn.Name, generation)
	if aerr != nil {
		return aerr
	}
	if found {
		fn.CodeZip = zip
		return nil
	}
	// Nothing is stored for the generation this snapshot names — either it was
	// collected by a code update that committed after the record was read, or
	// the caller built the snapshot from an ARN alone and named no generation
	// at all. Materialize what the function is now, bytes and identity
	// together, rather than cold starting with no package or with bytes the
	// record's CodeSha256 and RevisionId do not describe.
	current, aerr := s.getFunctionIn(ctx, region, fn.Name)
	if aerr != nil || current == nil {
		return aerr // deleted meanwhile; nothing coherent left to load
	}
	currentGeneration := functionCodeGeneration(current)
	if currentGeneration == "" || currentGeneration == generation {
		return nil // no package deployed under the generation now current
	}
	zip, found, aerr = s.readFunctionPackage(ctx, region, fn.Name, currentGeneration)
	if aerr != nil || !found {
		return aerr
	}
	fn.setCodeBytes(zip)
	fn.CodeGeneration = current.CodeGeneration
	return nil
}

// readFunctionPackage materializes one generation of a function's package.
// A miss on the generation key falls back to the pre-generation key, where
// packages written before generation addressing still live.
func (s *lambdaStore) readFunctionPackage(ctx context.Context, region, name, generation string) ([]byte, bool, *protocol.AWSError) {
	keys := make([]string, 0, 2)
	if generation != "" {
		keys = append(keys, serviceutil.RegionKey(region, functionCodeKey(name, generation)))
	}
	keys = append(keys, serviceutil.RegionKey(region, name))
	for _, key := range keys {
		raw, found, err := s.store.Get(ctx, nsFunctionCode, key)
		if err != nil {
			return nil, false, protocol.Wrap(protocol.ErrInternalError, err)
		}
		if !found || raw == "" {
			continue
		}
		zip, decErr := base64.StdEncoding.DecodeString(raw)
		if decErr != nil {
			return nil, false, protocol.Wrap(protocol.ErrInternalError, fmt.Errorf("lambda: decode stored package for %q: %w", name, decErr))
		}
		return zip, true, nil
	}
	return nil, false, nil
}

// deleteFunction removes a function and every generation of its stored package.
func (s *lambdaStore) deleteFunction(ctx context.Context, name string) *protocol.AWSError {
	s.mu.Lock()
	defer s.mu.Unlock()
	region := s.region(ctx)
	key := serviceutil.RegionKey(region, name)
	policies, err := s.store.Scan(ctx, nsFunctionPolicies, serviceutil.RegionKey(region, name+"|"))
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	for _, policy := range policies {
		if err := s.store.Delete(ctx, nsFunctionPolicies, policy.Key); err != nil {
			return protocol.Wrap(protocol.ErrInternalError, err)
		}
	}
	if err := s.pruneFunctionPackages(ctx, region, name, ""); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	// Delete the function record last. If cleanup fails before this point the
	// physical resource remains addressable and CloudFormation can retry.
	if err := s.store.Delete(ctx, nsFunctions, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func policyStoreKey(name, qualifier string) string { return name + "|" + qualifier }

// getFunctionPolicy isolates malformed policy records as absent. A corrupt
// policy must not make its owning function unreadable or break unrelated
// qualifiers; a subsequent AddPermission can repair the isolated record.
func (s *lambdaStore) getFunctionPolicy(ctx context.Context, name, qualifier string) (functionPolicy, bool, *protocol.AWSError) {
	raw, found, err := s.store.Get(ctx, nsFunctionPolicies, serviceutil.RegionKey(s.region(ctx), policyStoreKey(name, qualifier)))
	if err != nil {
		return functionPolicy{}, false, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return functionPolicy{}, false, nil
	}
	var policy functionPolicy
	if json.Unmarshal([]byte(raw), &policy) != nil {
		return functionPolicy{}, false, nil
	}
	return policy, true, nil
}

// mutateFunctionPolicy serializes read-check-write under Lambda's store lock,
// making RevisionId preconditions and concurrent statement changes atomic.
func (s *lambdaStore) mutateFunctionPolicy(ctx context.Context, name, qualifier string, mutate func(*functionPolicy, bool) (bool, *protocol.AWSError)) *protocol.AWSError {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, found, err := s.store.Get(ctx, nsFunctions, serviceutil.RegionKey(s.region(ctx), name)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	} else if !found {
		return policyFunctionNotFound(name, qualifier)
	}
	if qualifier != "" && qualifier != "$LATEST" {
		if _, err := strconv.Atoi(qualifier); err != nil {
			if _, found, err := s.store.Get(ctx, nsAliases, serviceutil.RegionKey(s.region(ctx), aliasKey(name, qualifier))); err != nil {
				return protocol.Wrap(protocol.ErrInternalError, err)
			} else if !found {
				return policyFunctionNotFound(name, qualifier)
			}
		}
	}
	policy, found, aerr := s.getFunctionPolicy(ctx, name, qualifier)
	if aerr != nil {
		return aerr
	}
	remove, aerr := mutate(&policy, found)
	if aerr != nil {
		return aerr
	}
	key := serviceutil.RegionKey(s.region(ctx), policyStoreKey(name, qualifier))
	if remove {
		if err := s.store.Delete(ctx, nsFunctionPolicies, key); err != nil {
			return protocol.Wrap(protocol.ErrInternalError, err)
		}
		return nil
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.store.Set(ctx, nsFunctionPolicies, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// listFunctions returns all stored functions.
func (s *lambdaStore) listFunctions(ctx context.Context) ([]*Function, *protocol.AWSError) {
	kvs, err := s.store.Scan(ctx, nsFunctions, serviceutil.RegionKey(s.region(ctx), ""))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	out := make([]*Function, 0, len(kvs))
	for _, kv := range kvs {
		var fn Function
		if err := json.Unmarshal([]byte(kv.Value), &fn); err != nil {
			continue // skip corrupt entries
		}
		out = append(out, &fn)
	}
	return out, nil
}

// listAllFunctions returns all stored functions across all regions.
// Used by startup reconciliation which must not be scoped to a single region.
func (s *lambdaStore) listAllFunctions(ctx context.Context) ([]*Function, *protocol.AWSError) {
	kvs, err := s.store.Scan(ctx, nsFunctions, "")
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	out := make([]*Function, 0, len(kvs))
	for _, kv := range kvs {
		var fn Function
		if err := json.Unmarshal([]byte(kv.Value), &fn); err != nil {
			continue // skip corrupt entries
		}
		out = append(out, &fn)
	}
	return out, nil
}

// invocationRecord captures metadata written each time InvokeAsync runs.
type invocationRecord struct {
	FunctionName string `json:"function_name"`
	FunctionARN  string `json:"function_arn"`
	PayloadSize  int    `json:"payload_size"`
}

// addInvocation writes an invocation record, keyed by "{name}:{nano_timestamp}".
// It is used to make notification delivery observable in tests.
func (s *lambdaStore) addInvocation(ctx context.Context, fn *Function, payload []byte) error {
	rec := invocationRecord{
		FunctionName: fn.Name,
		FunctionARN:  fn.ARN,
		PayloadSize:  len(payload),
	}
	raw, _ := json.Marshal(rec)
	// Use a unique key per invocation so multiple calls are all preserved.
	key := fn.Name + ":" + uniqueSuffix(s.clk)
	return s.store.Set(ctx, nsInvocations, serviceutil.RegionKey(s.region(ctx), key), string(raw))
}

// invocationSeq is a monotonic counter used to ensure unique invocation keys
// even when two invocations happen within the same nanosecond.
var invocationSeq atomic.Int64

// uniqueSuffix returns a string that is unique per process run, combining
// the current nanosecond timestamp and a monotonic counter.
func uniqueSuffix(clk clock.Clock) string {
	seq := invocationSeq.Add(1)
	return strconv.FormatInt(clk.Now().UnixNano(), 10) + "-" + strconv.FormatInt(seq, 10)
}

// functionNameFromARN parses the function name from a full Lambda ARN.
// ARN format: arn:aws:lambda:<region>:<account>:function:<name>
// Also handles unqualified names and partial ARNs gracefully.
func functionNameFromARN(arn string) string {
	if !strings.HasPrefix(arn, "arn:") {
		// Treat bare names as-is.
		return arn
	}
	parts := strings.Split(arn, ":")
	// arn:aws:lambda:region:account:function:name → index 6
	if len(parts) >= 7 && parts[5] == "function" {
		return parts[6]
	}
	return arn
}

// regionFromFunctionARN extracts the region segment from a full Lambda ARN.
// Returns "" for unqualified names or malformed ARNs so callers can fall back
// to the emulator's default region.
func regionFromFunctionARN(arn string) string {
	if !strings.HasPrefix(arn, "arn:") {
		return ""
	}
	parts := strings.Split(arn, ":")
	// arn:aws:lambda:region:account:function:name → region at index 3
	if len(parts) >= 4 {
		return parts[3]
	}
	return ""
}

// ─── Saved test events ─────────────────────────────────────────────────────

// TestEvent is a saved test event payload associated with a Lambda function.
type TestEvent struct {
	Name         string `json:"name"`
	FunctionName string `json:"function_name"`
	Body         string `json:"body"` // JSON event payload
}

// testEventKey builds the store key for a test event: "{functionName}:{eventName}".
func testEventKey(functionName, eventName string) string {
	return functionName + ":" + eventName
}

// listTestEvents returns all saved test events for a function.
func (s *lambdaStore) listTestEvents(ctx context.Context, functionName string) ([]TestEvent, error) {
	kvs, err := s.store.Scan(ctx, nsTestEvents, serviceutil.RegionKey(s.region(ctx), functionName+":"))
	if err != nil {
		return nil, err
	}
	out := make([]TestEvent, 0, len(kvs))
	for _, kv := range kvs {
		var te TestEvent
		if err := json.Unmarshal([]byte(kv.Value), &te); err != nil {
			continue
		}
		out = append(out, te)
	}
	return out, nil
}

// putTestEvent saves or updates a test event.
func (s *lambdaStore) putTestEvent(ctx context.Context, te *TestEvent) error {
	raw, err := json.Marshal(te)
	if err != nil {
		return err
	}
	return s.store.Set(ctx, nsTestEvents, serviceutil.RegionKey(s.region(ctx), testEventKey(te.FunctionName, te.Name)), string(raw))
}

// deleteTestEvent removes a saved test event.
func (s *lambdaStore) deleteTestEvent(ctx context.Context, functionName, eventName string) error {
	return s.store.Delete(ctx, nsTestEvents, serviceutil.RegionKey(s.region(ctx), testEventKey(functionName, eventName)))
}

// ─── Versions ──────────────────────────────────────────────────────────────

const (
	// nsVersions stores immutable function version snapshots.
	// Keys are "{functionName}:{versionNumber:010d}" (zero-padded so Scan returns
	// them in numeric order).
	nsVersions = "lambda:versions"

	// nsVersionCounters stores the next version number per function.
	// Keys are "{functionName}", values are decimal integers.
	nsVersionCounters = "lambda:version-counters"
)

// FunctionVersion is an immutable snapshot of a function configuration published
// via PublishVersion. It mirrors the AWS FunctionConfiguration wire shape with the
// additional Version and CodeSha256 fields.
type FunctionVersion struct {
	// Embed the full function config — all fields are frozen at publish time.
	Function
	// Version is the numeric version identifier (1, 2, 3, …).
	Version int `json:"version"`
	// Description overrides the function description for this specific version.
	Description string `json:"version_description,omitempty"`
	// CodeSha256 is the SHA-256 of the deployment package at publish time.
	CodeSha256 string `json:"code_sha256,omitempty"`
}

// versionKey returns the store key for a version, zero-padded so Scan returns
// versions in numeric order.
func versionKey(functionName string, version int) string {
	return fmt.Sprintf("%s:%010d", functionName, version)
}

// publishVersion allocates the next version number for v's function, stamps
// it on v and stores the snapshot, in one critical section. It is the one
// path a numbered version is written through — PublishVersion directly, and
// CreateFunction with Publish=true via createFunctionPublishing, which holds
// the lock already and calls publishVersionLocked.
func (s *lambdaStore) publishVersion(ctx context.Context, v *FunctionVersion) *protocol.AWSError {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publishVersionLocked(ctx, v)
}

// publishVersionLocked is publishVersion for a caller that holds s.mu.
func (s *lambdaStore) publishVersionLocked(ctx context.Context, v *FunctionVersion) *protocol.AWSError {
	versionNum, err := s.nextVersionLocked(ctx, v.Name)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	v.Version = versionNum
	return s.putVersion(ctx, v)
}

// nextVersionLocked increments and returns the next version number for a
// function. Version numbers start at 1. The caller holds s.mu, which is what
// makes the read-increment-write atomic.
func (s *lambdaStore) nextVersionLocked(ctx context.Context, functionName string) (int, error) {
	key := serviceutil.RegionKey(s.region(ctx), functionName)
	raw, found, err := s.store.Get(ctx, nsVersionCounters, key)
	if err != nil {
		return 0, fmt.Errorf("lambda: read version counter %q: %w", functionName, err)
	}
	next := 1
	if found && raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return 0, fmt.Errorf("lambda: parse version counter %q: %w", functionName, err)
		}
		next = n + 1
	}
	if err := s.store.Set(ctx, nsVersionCounters, key, strconv.Itoa(next)); err != nil {
		return 0, fmt.Errorf("lambda: save version counter %q: %w", functionName, err)
	}
	return next, nil
}

// putVersion stores an immutable function version.
func (s *lambdaStore) putVersion(ctx context.Context, v *FunctionVersion) *protocol.AWSError {
	raw, err := json.Marshal(v)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.store.Set(ctx, nsVersions, serviceutil.RegionKey(s.region(ctx), versionKey(v.Name, v.Version)), string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// listVersions returns all published versions for a function in ascending order.
func (s *lambdaStore) listVersions(ctx context.Context, functionName string) ([]*FunctionVersion, *protocol.AWSError) {
	kvs, err := s.store.Scan(ctx, nsVersions, serviceutil.RegionKey(s.region(ctx), functionName+":"))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	out := make([]*FunctionVersion, 0, len(kvs))
	for _, kv := range kvs {
		var v FunctionVersion
		if err := json.Unmarshal([]byte(kv.Value), &v); err != nil {
			continue
		}
		out = append(out, &v)
	}
	return out, nil
}

// deleteVersion removes one published version along with the state scoped to
// that qualifier: its resource policy and its provisioned-concurrency config.
// The version counter is deliberately left alone — AWS never reuses a version
// number, so the next PublishVersion must not fall back onto a deleted one.
func (s *lambdaStore) deleteVersion(ctx context.Context, functionName string, version int) *protocol.AWSError {
	s.mu.Lock()
	defer s.mu.Unlock()
	region := s.region(ctx)
	qualifier := strconv.Itoa(version)
	if err := s.store.Delete(ctx, nsFunctionPolicies, serviceutil.RegionKey(region, policyStoreKey(functionName, qualifier))); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if aerr := s.deleteProvisionedConcurrencyConfig(ctx, functionName, qualifier); aerr != nil {
		return aerr
	}
	// The version record goes last. If cleanup fails before this point the
	// version is still addressable and the delete can simply be retried.
	if err := s.store.Delete(ctx, nsVersions, serviceutil.RegionKey(region, versionKey(functionName, version))); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// ─── Aliases ───────────────────────────────────────────────────────────────

const (
	// nsAliases stores function aliases.
	// Keys are "{functionName}:{aliasName}".
	nsAliases = "lambda:aliases"
)

// FunctionAlias is a named pointer to a specific function version.
type FunctionAlias struct {
	FunctionName    string `json:"function_name"`
	Name            string `json:"name"`
	FunctionVersion string `json:"function_version"` // e.g. "3" or "$LATEST"
	Description     string `json:"description,omitempty"`
	AliasARN        string `json:"alias_arn"`
	RevisionId      string `json:"revision_id"`
}

// aliasKey returns the store key for an alias.
func aliasKey(functionName, aliasName string) string {
	return functionName + ":" + aliasName
}

// putAlias stores or updates an alias.
func (s *lambdaStore) putAlias(ctx context.Context, a *FunctionAlias) *protocol.AWSError {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.Marshal(a)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.store.Set(ctx, nsAliases, serviceutil.RegionKey(s.region(ctx), aliasKey(a.FunctionName, a.Name)), string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// getAlias retrieves an alias by function name + alias name. Returns nil, nil when not found.
func (s *lambdaStore) getAlias(ctx context.Context, functionName, aliasName string) (*FunctionAlias, *protocol.AWSError) {
	raw, found, err := s.store.Get(ctx, nsAliases, serviceutil.RegionKey(s.region(ctx), aliasKey(functionName, aliasName)))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, nil
	}
	var a FunctionAlias
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, fmt.Errorf("lambda: decode alias %q/%q: %w", functionName, aliasName, err))
	}
	return &a, nil
}

// listAliases returns all aliases for a function.
func (s *lambdaStore) listAliases(ctx context.Context, functionName string) ([]*FunctionAlias, *protocol.AWSError) {
	kvs, err := s.store.Scan(ctx, nsAliases, serviceutil.RegionKey(s.region(ctx), functionName+":"))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	out := make([]*FunctionAlias, 0, len(kvs))
	for _, kv := range kvs {
		var a FunctionAlias
		if err := json.Unmarshal([]byte(kv.Value), &a); err != nil {
			continue
		}
		out = append(out, &a)
	}
	return out, nil
}

// deleteAlias removes an alias.
func (s *lambdaStore) deleteAlias(ctx context.Context, functionName, aliasName string) *protocol.AWSError {
	s.mu.Lock()
	defer s.mu.Unlock()
	policyKey := serviceutil.RegionKey(s.region(ctx), policyStoreKey(functionName, aliasName))
	if err := s.store.Delete(ctx, nsFunctionPolicies, policyKey); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.store.Delete(ctx, nsAliases, serviceutil.RegionKey(s.region(ctx), aliasKey(functionName, aliasName))); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// ─── Layers ────────────────────────────────────────────────────────────────

const (
	// nsLayers stores layer version records.
	// Keys are "{layerName}:{version:010d}" — zero-padded so Scan returns versions
	// in numeric order.
	nsLayers = "lambda:layers"

	// nsLayerContent holds each layer version's zip (base64), split from the
	// record for the same reason function packages are (nsFunctionCode):
	// ListLayers / ListLayerVersions decode every record they scan, and with
	// the zip embedded that decoded every layer's full archive per call.
	// Keys match nsLayers. Layer versions are immutable, so records written
	// before the split simply keep their embedded content — readers handle
	// both shapes.
	nsLayerContent = "lambda:layer-content"

	// nsLayerCounters stores the next version number per layer name.
	// Keys are "{layerName}", values are decimal integers.
	nsLayerCounters = "lambda:layer-counters"
)

// LayerVersion is the domain model for a published Lambda layer version.
type LayerVersion struct {
	LayerName               string   `json:"layer_name"`
	LayerARN                string   `json:"layer_arn"`
	LayerVersionARN         string   `json:"layer_version_arn"`
	Version                 int64    `json:"version"`
	Description             string   `json:"description,omitempty"`
	CreatedDate             string   `json:"created_date"`
	CompatibleRuntimes      []string `json:"compatible_runtimes,omitempty"`
	CompatibleArchitectures []string `json:"compatible_architectures,omitempty"`
	// Content is the layer zip. Persisted under its own key (nsLayerContent)
	// and stripped from the record by putLayerVersion; call loadLayerContent
	// before paths that need the bytes. Records persisted before the split
	// still carry it inline.
	Content []byte `json:"content,omitempty"`
	// CodeSize is the byte length of Content.
	CodeSize int64 `json:"code_size"`
	// CodeHash is the hex SHA-256 of Content, set at publish. Wire responses
	// report it in AWS's base64 form (Content.CodeSha256); "" on records
	// published before the field existed.
	CodeHash string `json:"code_hash,omitempty"`
}

// setContent installs the layer zip, keeping CodeSize and CodeHash in step —
// the layer twin of Function.setCode.
func (lv *LayerVersion) setContent(zip []byte) {
	lv.Content = zip
	lv.CodeSize = int64(len(zip))
	lv.CodeHash = codeHashOf(zip)
}

// layerVersionKey returns the store key for a layer version, zero-padded for
// ordered Scan results.
func layerVersionKey(layerName string, version int64) string {
	return fmt.Sprintf("%s:%010d", layerName, version)
}

// nextLayerVersion atomically increments and returns the next version number
// for a layer. Version numbers start at 1.
func (s *lambdaStore) nextLayerVersion(ctx context.Context, layerName string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := serviceutil.RegionKey(s.region(ctx), layerName)
	raw, found, err := s.store.Get(ctx, nsLayerCounters, key)
	if err != nil {
		return 0, fmt.Errorf("lambda: read layer version counter %q: %w", layerName, err)
	}
	var next int64 = 1
	if found && raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("lambda: parse layer version counter %q: %w", layerName, err)
		}
		next = n + 1
	}
	if err := s.store.Set(ctx, nsLayerCounters, key, strconv.FormatInt(next, 10)); err != nil {
		return 0, fmt.Errorf("lambda: save layer version counter %q: %w", layerName, err)
	}
	return next, nil
}

// putLayerVersion stores a layer version.
func (s *lambdaStore) putLayerVersion(ctx context.Context, lv *LayerVersion) *protocol.AWSError {
	record := *lv
	record.Content = nil
	raw, err := json.Marshal(&record)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), layerVersionKey(lv.LayerName, lv.Version))
	// Content before record, as with function packages: a stored record must
	// never point at content that was not stored.
	if len(lv.Content) > 0 {
		if err := s.store.Set(ctx, nsLayerContent, key, base64.StdEncoding.EncodeToString(lv.Content)); err != nil {
			return protocol.Wrap(protocol.ErrInternalError, err)
		}
	}
	if err := s.store.Set(ctx, nsLayers, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// loadLayerContent populates lv.Content from the stored zip. Records written
// before the split still embed their content and are left as is.
func (s *lambdaStore) loadLayerContent(ctx context.Context, lv *LayerVersion) *protocol.AWSError {
	if lv == nil || len(lv.Content) > 0 {
		return nil
	}
	// The layer's own region: cold starts can run on a bare context, and the
	// version ARN always carries the partition the layer was published to.
	region := serviceutil.ARNRegion(lv.LayerVersionARN)
	if region == "" {
		region = s.region(ctx)
	}
	raw, found, err := s.store.Get(ctx, nsLayerContent, serviceutil.RegionKey(region, layerVersionKey(lv.LayerName, lv.Version)))
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found || raw == "" {
		return nil
	}
	zip, decErr := base64.StdEncoding.DecodeString(raw)
	if decErr != nil {
		return protocol.Wrap(protocol.ErrInternalError, fmt.Errorf("lambda: decode stored layer content %s v%d: %w", lv.LayerName, lv.Version, decErr))
	}
	lv.Content = zip
	return nil
}

// getLayerVersion retrieves a specific layer version. Returns nil, nil when not found.
func (s *lambdaStore) getLayerVersion(ctx context.Context, layerName string, version int64) (*LayerVersion, *protocol.AWSError) {
	raw, found, err := s.store.Get(ctx, nsLayers, serviceutil.RegionKey(s.region(ctx), layerVersionKey(layerName, version)))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, nil
	}
	var lv LayerVersion
	if err := json.Unmarshal([]byte(raw), &lv); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, fmt.Errorf("lambda: decode layer %q v%d: %w", layerName, version, err))
	}
	return &lv, nil
}

// listLayerVersions returns all versions for a layer in ascending order.
func (s *lambdaStore) listLayerVersions(ctx context.Context, layerName string) ([]*LayerVersion, *protocol.AWSError) {
	kvs, err := s.store.Scan(ctx, nsLayers, serviceutil.RegionKey(s.region(ctx), layerName+":"))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	out := make([]*LayerVersion, 0, len(kvs))
	for _, kv := range kvs {
		var lv LayerVersion
		if err := json.Unmarshal([]byte(kv.Value), &lv); err != nil {
			continue
		}
		out = append(out, &lv)
	}
	return out, nil
}

// listAllLayerNames returns the distinct layer names that have at least one version.
// It scans the counters namespace which has one entry per layer name.
func (s *lambdaStore) listAllLayerNames(ctx context.Context) ([]string, *protocol.AWSError) {
	kvs, err := s.store.Scan(ctx, nsLayerCounters, serviceutil.RegionKey(s.region(ctx), ""))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	names := make([]string, 0, len(kvs))
	for _, kv := range kvs {
		_, rest := serviceutil.SplitRegionKey(kv.Key)
		names = append(names, rest)
	}
	return names, nil
}

// deleteLayerVersion removes a specific layer version.
func (s *lambdaStore) deleteLayerVersion(ctx context.Context, layerName string, version int64) *protocol.AWSError {
	key := serviceutil.RegionKey(s.region(ctx), layerVersionKey(layerName, version))
	if err := s.store.Delete(ctx, nsLayers, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.store.Delete(ctx, nsLayerContent, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// ─── Provisioned concurrency ───────────────────────────────────────────────

const (
	// nsProvisionedConcurrency stores provisioned concurrency configs.
	// Keys are "{functionName}:{qualifier}".
	nsProvisionedConcurrency = "lambda:provisioned-concurrency"
)

// ProvisionedConcurrencyConfig is the domain model for a provisioned concurrency setting.
type ProvisionedConcurrencyConfig struct {
	FunctionName                             string `json:"function_name"`
	Qualifier                                string `json:"qualifier"`
	RequestedProvisionedConcurrentExecutions int    `json:"requested"`
	LastModified                             string `json:"last_modified"`
}

// provisionedConcurrencyKey builds the store key for a provisioned concurrency config.
func provisionedConcurrencyKey(functionName, qualifier string) string {
	return functionName + ":" + qualifier
}

// putProvisionedConcurrencyConfig stores a provisioned concurrency config.
func (s *lambdaStore) putProvisionedConcurrencyConfig(ctx context.Context, cfg *ProvisionedConcurrencyConfig) *protocol.AWSError {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), provisionedConcurrencyKey(cfg.FunctionName, cfg.Qualifier))
	if err := s.store.Set(ctx, nsProvisionedConcurrency, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// getProvisionedConcurrencyConfig retrieves a provisioned concurrency config. Returns nil, nil when not found.
func (s *lambdaStore) getProvisionedConcurrencyConfig(ctx context.Context, functionName, qualifier string) (*ProvisionedConcurrencyConfig, *protocol.AWSError) {
	key := serviceutil.RegionKey(s.region(ctx), provisionedConcurrencyKey(functionName, qualifier))
	raw, found, err := s.store.Get(ctx, nsProvisionedConcurrency, key)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, nil
	}
	var cfg ProvisionedConcurrencyConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, fmt.Errorf("lambda: decode provisioned concurrency %q/%q: %w", functionName, qualifier, err))
	}
	return &cfg, nil
}

// deleteProvisionedConcurrencyConfig removes a provisioned concurrency config.
func (s *lambdaStore) deleteProvisionedConcurrencyConfig(ctx context.Context, functionName, qualifier string) *protocol.AWSError {
	key := serviceutil.RegionKey(s.region(ctx), provisionedConcurrencyKey(functionName, qualifier))
	if err := s.store.Delete(ctx, nsProvisionedConcurrency, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// listProvisionedConcurrencyConfigs returns every config for functionName in
// the request's region. A malformed record is skipped rather than failing the
// whole listing.
func (s *lambdaStore) listProvisionedConcurrencyConfigs(ctx context.Context, functionName string) ([]*ProvisionedConcurrencyConfig, *protocol.AWSError) {
	kvs, err := s.store.Scan(ctx, nsProvisionedConcurrency, serviceutil.RegionKey(s.region(ctx), functionName+":"))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	out := make([]*ProvisionedConcurrencyConfig, 0, len(kvs))
	for _, kv := range kvs {
		var cfg ProvisionedConcurrencyConfig
		if err := json.Unmarshal([]byte(kv.Value), &cfg); err != nil {
			continue
		}
		out = append(out, &cfg)
	}
	return out, nil
}

// regionalProvisionedConfig pairs a reservation with the region it was created
// in, which the config record itself does not carry.
type regionalProvisionedConfig struct {
	Region string
	Config *ProvisionedConcurrencyConfig
}

// listAllProvisionedConcurrencyConfigs returns every config across all regions,
// each tagged with its region. Used at startup to rebuild reservations after a
// restart.
func (s *lambdaStore) listAllProvisionedConcurrencyConfigs(ctx context.Context) ([]regionalProvisionedConfig, *protocol.AWSError) {
	kvs, err := s.store.Scan(ctx, nsProvisionedConcurrency, "")
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	out := make([]regionalProvisionedConfig, 0, len(kvs))
	for _, kv := range kvs {
		var cfg ProvisionedConcurrencyConfig
		if err := json.Unmarshal([]byte(kv.Value), &cfg); err != nil {
			continue
		}
		region, _ := serviceutil.SplitRegionKey(kv.Key)
		out = append(out, regionalProvisionedConfig{Region: region, Config: &cfg})
	}
	return out, nil
}

// getLayerVersionByARN parses a layer version ARN of the form
// arn:aws:lambda:{region}:{account}:layer:{name}:{version} and delegates to
// getLayerVersion. Returns nil, nil when the ARN has an unexpected shape or the
// layer version does not exist.
func (s *lambdaStore) getLayerVersionByARN(ctx context.Context, arn string) (*LayerVersion, *protocol.AWSError) {
	// Minimum expected ARN has 8 colon-separated segments.
	parts := strings.Split(arn, ":")
	if len(parts) < 8 {
		return nil, nil
	}
	layerName := parts[6]
	versionStr := parts[7]
	version, err := strconv.ParseInt(versionStr, 10, 64)
	if err != nil {
		return nil, nil
	}
	return s.getLayerVersion(ctx, layerName, version)
}
