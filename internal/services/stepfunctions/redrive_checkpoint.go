package stepfunctions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// Redrive checkpoints: where an unsuccessful run stopped, down inside its
// Parallel and Map states.
//
// Every frame the interpreter runs — the definition's own States, each
// Parallel branch and each Map iteration — notes the state it stopped in
// (failed, timed out or aborted) and the raw input and variables it entered
// that state with. When that state is a Parallel or a Map the note also
// carries the outcome of each branch or iteration of its last attempt: the
// output of those that succeeded, and the note of those that did not, which
// makes the whole thing a tree. A distributed Map notes its map run instead —
// the child execution behind each item and the input of any child that never
// started.
//
// RedriveExecution resumes from that tree, as AWS does: a Parallel or Map is
// re-entered, the branches and iterations that succeeded contribute their
// recorded outputs without running again, and the rest resume at the state
// they stopped in (or start from the top, if they never entered one). A
// distributed Map redrives its map run — see redriveMapRun.
//
// The root's state, input and variables are also on the Execution record
// (RedriveState and friends), which is what eligibility reads. The tree
// itself is stored under its own key, only when there is a container below
// the root, so ListExecutions never decodes it. A tree that cannot be decoded
// is ignored: the redrive then re-runs the whole Parallel or Map, which is
// what Overcast did before checkpoints existed.

const redriveCheckpointPrefix = "redrive:"

// maxMapRunRedrives is AWS's hard limit on redrives of one map run.
const maxMapRunRedrives = 1000

// errDataLimitExceeded is the one failure AWS re-runs a whole Parallel or Map
// for, successful children included.
const errDataLimitExceeded = "States.DataLimitExceeded"

// redrivePoint is where one frame stopped without succeeding.
type redrivePoint struct {
	// State is the state the frame stopped in, Input the raw input it entered
	// that state with, and Variables the variables in scope then.
	State     string `json:"state"`
	Input     string `json:"input"`
	Variables string `json:"variables,omitempty"`
	// Branches is, when State is a Parallel or an inline Map, the outcome of
	// each branch or iteration of its last attempt, by index.
	Branches []redriveChild `json:"branches,omitempty"`
	// MapRun is set when State is a distributed Map whose map run started.
	MapRun *redriveMapRun `json:"mapRun,omitempty"`
}

// hasContainer reports whether the point records the inside of a Parallel or
// Map — the part a redrive can resume inside.
func (p *redrivePoint) hasContainer() bool {
	return p != nil && (len(p.Branches) > 0 || p.MapRun != nil)
}

// redriveChild is how one Parallel branch or inline Map iteration ended.
// Neither Succeeded nor Point means it never entered a state (an iteration
// that never started, or one whose ItemSelector failed): a redrive runs it
// from the top.
type redriveChild struct {
	Succeeded bool          `json:"succeeded,omitempty"`
	Output    string        `json:"output,omitempty"`
	Point     *redrivePoint `json:"point,omitempty"`
}

// redriveMapRun is the part of a distributed Map a redrive needs: which child
// execution ran each input, and the inputs of those that never started.
type redriveMapRun struct {
	MapRunArn string `json:"mapRunArn"`
	// Children holds each child execution's ARN in input order; an empty
	// entry is a child that never started.
	Children []string `json:"children"`
	// ItemsPer is how many items each child holds (more than one with an
	// ItemBatcher).
	ItemsPer []int `json:"itemsPer"`
	// Pending is the encoded input of each child that never started, by index.
	Pending map[int]string `json:"pending,omitempty"`
}

// redriveContainer is what the Parallel or Map a frame ran last left behind,
// held until the frame learns whether that state failed.
type redriveContainer struct {
	state    string
	branches []redriveChild
	mapRun   *redriveMapRun
}

// ─── Frame bookkeeping ────────────────────────────────────────────────────────

// noteFailure records the state this frame stopped in without succeeding —
// failed, timed out or aborted — with the raw input it was entered with and,
// when it is the Parallel or Map the frame just ran, what happened inside it.
// The first note wins. The top-level frame's note is also what the Execution
// record's RedriveState is set from.
func (in *interpreter) noteFailure(name string, raw any, serr *stateError) {
	if in.point != nil || in.testState != nil {
		return
	}
	encoded, err := encodeJSON(raw)
	if err != nil {
		return
	}
	point := &redrivePoint{State: name, Input: encoded, Variables: in.vars.snapshot()}
	if c := in.container; c != nil && c.state == name && (serr == nil || serr.name != errDataLimitExceeded) {
		point.Branches, point.MapRun = c.branches, c.mapRun
	}
	in.point = point
	if in.topLevel && in.run != nil {
		in.run.noteFailure(name, encoded, point.Variables)
	}
}

// beginState resets the per-state redrive bookkeeping as a frame enters a
// state, and hands the state the inside of the Parallel or Map it resumes, if
// this is the state a redrive resumes the frame at.
func (in *interpreter) beginState(name string) {
	in.container = nil
	in.resumeContainer = nil
	if r := in.resume; r != nil {
		in.resume = nil
		if r.State == name && r.hasContainer() {
			in.resumeContainer = r
		}
	}
}

// takeResume returns the checkpoint a Parallel or Map resumes from on its
// first attempt after a redrive, or nil. A retry of the same state runs it
// afresh.
func (in *interpreter) takeResume() *redrivePoint {
	r := in.resumeContainer
	in.resumeContainer = nil
	return r
}

// resumeAt prepares a branch or iteration frame to resume where p says it
// stopped, and returns the state and input it resumes with. ok is false when
// the input no longer decodes, in which case the child runs from the top.
func (in *interpreter) resumeAt(p *redrivePoint) (state string, input any, ok bool) {
	if p == nil || p.State == "" {
		return "", nil, false
	}
	decoded, err := decodeExecutionInput(p.Input)
	if err != nil {
		return "", nil, false
	}
	if p.Variables != "" {
		var values map[string]any
		if json.Unmarshal([]byte(p.Variables), &values) == nil {
			in.vars.assign(values)
		}
	}
	in.resume = p
	return p.State, decoded, true
}

// childOutcome is how a finished branch or iteration is checkpointed.
func childOutcome(child *interpreter, output any, serr *stateError) redriveChild {
	if serr != nil {
		return redriveChild{Point: child.point}
	}
	encoded, err := encodeJSON(output)
	if err != nil {
		return redriveChild{Point: child.point}
	}
	return redriveChild{Succeeded: true, Output: encoded}
}

// succeededOutput returns the recorded output of a branch or iteration that
// succeeded, and whether there is one to reuse.
func (p *redrivePoint) succeededOutput(i int) (any, bool) {
	if p == nil || i >= len(p.Branches) || !p.Branches[i].Succeeded {
		return nil, false
	}
	var out any
	if json.Unmarshal([]byte(p.Branches[i].Output), &out) != nil {
		return nil, false
	}
	return out, true
}

// childPoint returns where branch or iteration i stopped, or nil.
func (p *redrivePoint) childPoint(i int) *redrivePoint {
	if p == nil || i >= len(p.Branches) {
		return nil
	}
	return p.Branches[i].Point
}

// ─── Store ────────────────────────────────────────────────────────────────────

var errMalformedCheckpoint = errors.New("stepfunctions: malformed redrive checkpoint")

func redriveCheckpointKey(execARN string) string { return redriveCheckpointPrefix + execARN }

// putRedriveCheckpoint saves the tree of an execution's last unsuccessful run.
func (st *Store) putRedriveCheckpoint(ctx context.Context, execARN string, point *redrivePoint) error {
	raw, err := json.Marshal(point)
	if err != nil {
		return fmt.Errorf("stepfunctions: marshal redrive checkpoint %q: %w", execARN, err)
	}
	return st.s.Set(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), redriveCheckpointKey(execARN)), string(raw))
}

// getRedriveCheckpoint returns an execution's checkpoint tree, nil when there
// is none, and errMalformedCheckpoint (wrapped) when the stored record does
// not decode.
func (st *Store) getRedriveCheckpoint(ctx context.Context, execARN string) (*redrivePoint, error) {
	raw, found, err := st.s.Get(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), redriveCheckpointKey(execARN)))
	if err != nil {
		return nil, fmt.Errorf("stepfunctions: get redrive checkpoint %q: %w", execARN, err)
	}
	if !found {
		return nil, nil
	}
	var point redrivePoint
	if err := json.Unmarshal([]byte(raw), &point); err != nil {
		return nil, fmt.Errorf("%w %q: %v", errMalformedCheckpoint, execARN, err)
	}
	return &point, nil
}

// deleteRedriveCheckpoint removes an execution's checkpoint tree.
func (st *Store) deleteRedriveCheckpoint(ctx context.Context, execARN string) error {
	return st.s.Delete(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), redriveCheckpointKey(execARN)))
}

// saveCheckpoint persists or clears the checkpoint tree after a run ends.
// Only a tree with a container below its root is stored; anything else is
// fully described by the Execution record. A run that was itself a redrive,
// or a re-started child, may have left an older tree behind, which is removed.
func (h *Handler) saveCheckpoint(ctx context.Context, exec *Execution, run *executionRun, succeeded bool) error {
	if !succeeded && run.checkpoint.hasContainer() {
		return h.store.putRedriveCheckpoint(ctx, exec.ExecutionArn, run.checkpoint)
	}
	if exec.RedriveCount > 0 || run.restarted {
		return h.store.deleteRedriveCheckpoint(ctx, exec.ExecutionArn)
	}
	return nil
}

// resumePoint is where a redrive of exec resumes: the root the Execution
// record names, with the stored tree below it when there is one that belongs
// to that root. A malformed tree is logged and ignored rather than failing
// the redrive — the Parallel or Map at the root is then re-run whole. nil
// means the run never entered a state and a redrive starts from the top.
func (h *Handler) resumePoint(ctx context.Context, exec *Execution) *redrivePoint {
	if exec.RedriveState == "" {
		return nil
	}
	point := &redrivePoint{State: exec.RedriveState, Input: exec.RedriveInput, Variables: exec.RedriveVariables}
	tree, err := h.store.getRedriveCheckpoint(ctx, exec.ExecutionArn)
	if err != nil {
		h.log.WithRecorder(ctx).Logger().Warn("stepfunctions: ignoring an unreadable redrive checkpoint; the failed Parallel or Map re-runs whole",
			zap.String("execution", exec.ExecutionArn), zap.Error(err))
		return point
	}
	if tree != nil && tree.State == point.State {
		point.Branches, point.MapRun = tree.Branches, tree.MapRun
	}
	return point
}
