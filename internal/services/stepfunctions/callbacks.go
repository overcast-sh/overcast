package stepfunctions

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// Task tokens: the `.waitForTaskToken` integration pattern and activity tasks
// both park a Task until an outside party calls SendTaskSuccess or
// SendTaskFailure with the token Step Functions issued, optionally keeping it
// alive with SendTaskHeartbeat.
//
// The registry below is in memory, but a token outlives the process that
// issued it: a top-level Task parked on its token writes a checkpoint
// (durable.go), and after a restart the execution is rehydrated and its token
// registered again with restore, so SendTaskSuccess/SendTaskFailure/
// SendTaskHeartbeat and GetActivityTask keep working.

// taskCallback is what a worker reported for a token.
type taskCallback struct {
	output  string
	failed  bool
	errName string
	cause   string
}

// pendingTask is one Task waiting on its token.
type pendingTask struct {
	token string
	// activityArn is set for an activity task; empty for .waitForTaskToken.
	activityArn string
	// input is the JSON an activity worker receives from GetActivityTask.
	input string

	result    chan taskCallback
	heartbeat chan struct{}
	picked    chan string
}

// pickUp tells the waiting interpreter a worker took the activity task.
func (t *pendingTask) pickUp(worker string) {
	select {
	case t.picked <- worker:
	default:
	}
}

// taskRegistry holds every live token and every activity task no worker has
// picked up yet.
type taskRegistry struct {
	mu      sync.Mutex
	pending map[string]*pendingTask
	queues  map[string][]*pendingTask
	signals map[string]chan struct{}
}

func newTaskRegistry() *taskRegistry {
	return &taskRegistry{
		pending: map[string]*pendingTask{},
		queues:  map[string][]*pendingTask{},
		signals: map[string]chan struct{}{},
	}
}

// newTaskToken returns an opaque token. AWS's tokens are long base64 strings;
// these are 64 random bytes in the same alphabet.
func newTaskToken() string {
	buf := make([]byte, 64)
	_, _ = rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

// register issues a token for a Task and makes it answerable.
func (r *taskRegistry) register(activityArn, input string) *pendingTask {
	return r.restore(newTaskToken(), activityArn, input)
}

// restore makes a token issued before a restart answerable again. It is
// register with the token supplied rather than minted.
func (r *taskRegistry) restore(token, activityArn, input string) *pendingTask {
	task := &pendingTask{
		token:       token,
		activityArn: activityArn,
		input:       input,
		result:      make(chan taskCallback, 1),
		heartbeat:   make(chan struct{}, 1),
		picked:      make(chan string, 1),
	}
	r.mu.Lock()
	r.pending[task.token] = task
	r.mu.Unlock()
	return task
}

// release forgets a token once its Task has finished, however it finished,
// and takes an unclaimed activity task off its queue.
func (r *taskRegistry) release(task *pendingTask) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pending, task.token)
	if task.activityArn == "" {
		return
	}
	queue := r.queues[task.activityArn]
	for i, queued := range queue {
		if queued == task {
			r.queues[task.activityArn] = append(queue[:i:i], queue[i+1:]...)
			break
		}
	}
}

// enqueue offers an activity task to the next worker that polls for it.
func (r *taskRegistry) enqueue(task *pendingTask) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queues[task.activityArn] = append(r.queues[task.activityArn], task)
	if signal, ok := r.signals[task.activityArn]; ok {
		close(signal)
		delete(r.signals, task.activityArn)
	}
}

// poll takes the oldest queued task for an activity, waiting until one is
// enqueued, the caller goes away, or expired fires. nil means no task.
func (r *taskRegistry) poll(ctx context.Context, activityArn string, expired <-chan time.Time) *pendingTask {
	for {
		r.mu.Lock()
		if queue := r.queues[activityArn]; len(queue) > 0 {
			task := queue[0]
			r.queues[activityArn] = queue[1:]
			r.mu.Unlock()
			return task
		}
		signal, ok := r.signals[activityArn]
		if !ok {
			signal = make(chan struct{})
			r.signals[activityArn] = signal
		}
		r.mu.Unlock()
		select {
		case <-signal:
		case <-ctx.Done():
			return nil
		case <-expired:
			return nil
		}
	}
}

// lookup returns the task a token names, or nil.
func (r *taskRegistry) lookup(token string) *pendingTask {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pending[token]
}

// complete delivers a worker's result. Only the first answer for a token is
// accepted; the token is forgotten as soon as it is answered.
func (r *taskRegistry) complete(token string, cb taskCallback) bool {
	r.mu.Lock()
	task := r.pending[token]
	delete(r.pending, token)
	r.mu.Unlock()
	if task == nil {
		return false
	}
	task.result <- cb
	return true
}

// ─── SendTaskSuccess / SendTaskFailure / SendTaskHeartbeat ────────────────────

// maxTaskTokenLength is the longest taskToken AWS accepts.
const maxTaskTokenLength = 2048

func errTaskDoesNotExist() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "TaskDoesNotExist",
		Message:    "Task Does Not Exist: the task token does not name a task that is waiting for a result",
		HTTPStatus: http.StatusBadRequest,
	}
}

func checkTaskToken(token string) *protocol.AWSError {
	if token == "" || len(token) > maxTaskTokenLength {
		return &protocol.AWSError{
			Code:       "InvalidToken",
			Message:    fmt.Sprintf("Invalid Token: '%s'", token),
			HTTPStatus: http.StatusBadRequest,
		}
	}
	return nil
}

type sendTaskSuccessRequest struct {
	TaskToken string `json:"taskToken" cbor:"taskToken"`
	Output    string `json:"output" cbor:"output"`
}

func (h *Handler) sendTaskSuccessTyped(_ context.Context, req *sendTaskSuccessRequest) (*struct{}, *protocol.AWSError) {
	if aerr := checkTaskToken(req.TaskToken); aerr != nil {
		return nil, aerr
	}
	if !json.Valid([]byte(req.Output)) {
		return nil, &protocol.AWSError{
			Code:       "InvalidOutput",
			Message:    "Invalid Output: the output is not valid JSON",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	if !h.tasks.complete(req.TaskToken, taskCallback{output: req.Output}) {
		return nil, errTaskDoesNotExist()
	}
	return &struct{}{}, nil
}

type sendTaskFailureRequest struct {
	TaskToken string `json:"taskToken" cbor:"taskToken"`
	Error     string `json:"error" cbor:"error"`
	Cause     string `json:"cause" cbor:"cause"`
}

func (h *Handler) sendTaskFailureTyped(_ context.Context, req *sendTaskFailureRequest) (*struct{}, *protocol.AWSError) {
	if aerr := checkTaskToken(req.TaskToken); aerr != nil {
		return nil, aerr
	}
	if !h.tasks.complete(req.TaskToken, taskCallback{failed: true, errName: req.Error, cause: req.Cause}) {
		return nil, errTaskDoesNotExist()
	}
	return &struct{}{}, nil
}

type sendTaskHeartbeatRequest struct {
	TaskToken string `json:"taskToken" cbor:"taskToken"`
}

func (h *Handler) sendTaskHeartbeatTyped(_ context.Context, req *sendTaskHeartbeatRequest) (*struct{}, *protocol.AWSError) {
	if aerr := checkTaskToken(req.TaskToken); aerr != nil {
		return nil, aerr
	}
	task := h.tasks.lookup(req.TaskToken)
	if task == nil {
		return nil, errTaskDoesNotExist()
	}
	select {
	case task.heartbeat <- struct{}{}:
	default:
	}
	return &struct{}{}, nil
}

// ─── Waiting on a token ───────────────────────────────────────────────────────

// awaitCallback blocks until the task's token is answered, its heartbeat
// lapses, or ctx ends. heartbeat is HeartbeatSeconds, nil when the state set
// none. A lapsed heartbeat is States.HeartbeatTimeout; a successful answer
// returns the worker's output decoded.
//
// park is the task's durable checkpoint, nil when the execution is not
// durable. A resumed task's first heartbeat window runs to the deadline the
// checkpoint recorded rather than a fresh HeartbeatSeconds, and each
// heartbeat moves that deadline (see parkHandle.noteHeartbeat).
func (in *interpreter) awaitCallback(ctx context.Context, task *pendingTask, heartbeat *int64, park *parkHandle) (any, *stateError) {
	var (
		timer   interface{ Stop() bool }
		expired <-chan time.Time
	)
	arm := func(d time.Duration) {
		if timer != nil {
			timer.Stop()
		}
		t := in.handler.clk.Timer(max(d, 0))
		timer, expired = t, t.C
	}
	if heartbeat != nil {
		window := time.Duration(*heartbeat) * time.Second
		if deadline := park.heartbeatDeadline(); deadline != nil {
			window = deadline.Sub(in.handler.clk.Now())
		}
		arm(window)
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	answer := func(cb taskCallback) (any, *stateError) {
		if cb.failed {
			return nil, &stateError{name: cb.errName, cause: cb.cause}
		}
		var decoded any
		if err := json.Unmarshal([]byte(cb.output), &decoded); err != nil {
			return nil, newStateError(errRuntime, "the task output is not valid JSON: %v", err)
		}
		return decoded, nil
	}
	for {
		select {
		case cb := <-task.result:
			return answer(cb)
		case <-task.heartbeat:
			if heartbeat == nil {
				continue
			}
			window := time.Duration(*heartbeat) * time.Second
			park.noteHeartbeat(ctx, in.handler.clk.Now().Add(window))
			arm(window)
		case <-expired:
			return nil, newStateError(errHeartbeatTimeout, "the task did not send a heartbeat within HeartbeatSeconds (%d)", *heartbeat)
		case <-ctx.Done():
			// An answer that raced the unwind has already been accepted —
			// complete forgot the token — so it must not be dropped.
			select {
			case cb := <-task.result:
				return answer(cb)
			default:
			}
			return nil, newStateError(errTimeout, "the task was interrupted while waiting for its token")
		}
	}
}

// withTaskToken returns a copy of the context object carrying $$.Task.Token.
func withTaskToken(ctxObj map[string]any, token string) map[string]any {
	out := make(map[string]any, len(ctxObj)+1)
	for k, v := range ctxObj {
		out[k] = v
	}
	out["Task"] = map[string]any{"Token": token}
	return out
}

// ─── Activity tasks ───────────────────────────────────────────────────────────

// runActivity runs one attempt of a Task whose Resource is an activity ARN:
// schedule it, wait for a worker to pick it up, then wait for that worker's
// answer. The history carries AWS's Activity* events rather than Task* ones.
func (in *interpreter) runActivity(ctx context.Context, f *flow, activityArn string) (any, *stateError) {
	name, state := f.name, f.state
	payload, serr := f.arguments()
	if serr != nil {
		return nil, serr
	}
	input, encErr := encodeJSON(payload)
	if encErr != nil {
		return nil, newStateError(errRuntime, "%s", encErr.Error())
	}
	timeout, serr := f.positiveSeconds(state.TimeoutSeconds, state.TimeoutSecondsPath, "TimeoutSeconds")
	if serr != nil {
		return nil, serr
	}
	heartbeat, serr := f.positiveSeconds(state.HeartbeatSeconds, state.HeartbeatSecondsPath, "HeartbeatSeconds")
	if serr != nil {
		return nil, serr
	}

	act, err := in.handler.store.GetActivity(ctx, activityNameFromARN(activityArn))
	if err != nil || act == nil || act.ARN != activityArn {
		failure := newStateError(errRuntime, "Activity Does Not Exist: '%s'", activityArn)
		in.record(HistoryEvent{Type: evtActivityScheduleFailed, ActivityScheduleFailed: &errorCauseDetails{Error: failure.name, Cause: failure.cause}})
		return nil, failure
	}

	in.record(HistoryEvent{
		Type: evtActivityScheduled,
		ActivityScheduled: &activityScheduledDetails{
			Resource:           activityArn,
			Input:              input,
			InputDetails:       &executionDataDetails{},
			TimeoutInSeconds:   timeout,
			HeartbeatInSeconds: heartbeat,
		},
	})

	taskCtx := ctx
	if timeout != nil {
		var cancel context.CancelFunc
		taskCtx, cancel = context.WithTimeout(ctx, time.Duration(*timeout)*time.Second)
		defer cancel()
	}

	task := in.handler.tasks.register(activityArn, input)
	defer in.handler.tasks.release(task)
	park := in.park(ctx, f, &executionCheckpoint{
		Kind:             parkActivity,
		Token:            task.token,
		ActivityArn:      activityArn,
		ActivityInput:    input,
		HeartbeatSeconds: heartbeat,
		TimeoutSeconds:   timeout,
		TimeoutDeadline:  deadlineAfter(in.handler.clk.Now(), timeout),
	})
	in.handler.tasks.enqueue(task)

	result, serr := in.awaitActivity(ctx, taskCtx, task, heartbeat, park)
	in.unpark(ctx, park, serr != nil && ctx.Err() != nil)
	return in.finishActivity(ctx, taskCtx, name, timeout, result, serr)
}

// awaitActivity waits for a worker to pick the activity task up — unless the
// checkpoint it resumes from says one already has — and then for its answer.
func (in *interpreter) awaitActivity(ctx, taskCtx context.Context, task *pendingTask, heartbeat *int64, park *parkHandle) (any, *stateError) {
	if !park.pickedUp() {
		select {
		case worker := <-task.picked:
			in.record(HistoryEvent{Type: evtActivityStarted, ActivityStarted: &activityStartedDetails{WorkerName: worker}})
			park.notePickedUp(ctx, worker, deadlineAfter(in.handler.clk.Now(), heartbeat))
		case <-taskCtx.Done():
			return nil, newStateError(errTimeout, "the activity task was not completed in time")
		}
	}
	return in.awaitCallback(taskCtx, task, heartbeat, park)
}

// finishActivity records how an activity attempt ended and returns its result.
func (in *interpreter) finishActivity(ctx, taskCtx context.Context, name string, timeout *int64, result any, serr *stateError) (any, *stateError) {
	if serr != nil {
		if ctx.Err() != nil {
			return nil, in.unwindReason(ctx, name)
		}
		if timeout != nil && taskCtx.Err() != nil {
			serr = newStateError(errTimeout, "the Task state %q exceeded its TimeoutSeconds of %d", name, *timeout)
		}
		details := &errorCauseDetails{Error: serr.name, Cause: serr.cause}
		if serr.name == errTimeout || serr.name == errHeartbeatTimeout {
			in.record(HistoryEvent{Type: evtActivityTimedOut, ActivityTimedOut: details})
		} else {
			in.record(HistoryEvent{Type: evtActivityFailed, ActivityFailed: details})
		}
		return nil, serr
	}
	encoded, _ := encodeJSON(result)
	in.record(HistoryEvent{
		Type:              evtActivitySucceeded,
		ActivitySucceeded: &lambdaSucceededDetails{Output: encoded, OutputDetails: &executionDataDetails{}},
	})
	return result, nil
}
