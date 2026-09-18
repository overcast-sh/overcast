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
// Tokens live only as long as the execution that is waiting on them, which is
// in memory — executions themselves are in-process goroutines, so a token that
// outlived its process would name nothing that could resume.

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
	task := &pendingTask{
		token:       newTaskToken(),
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
func (in *interpreter) awaitCallback(ctx context.Context, task *pendingTask, heartbeat *int64) (any, *stateError) {
	var (
		timer   interface{ Stop() bool }
		expired <-chan time.Time
	)
	arm := func() {
		if heartbeat == nil {
			return
		}
		if timer != nil {
			timer.Stop()
		}
		t := in.handler.clk.Timer(time.Duration(*heartbeat) * time.Second)
		timer, expired = t, t.C
	}
	arm()
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case cb := <-task.result:
			if cb.failed {
				return nil, &stateError{name: cb.errName, cause: cb.cause}
			}
			var decoded any
			if err := json.Unmarshal([]byte(cb.output), &decoded); err != nil {
				return nil, newStateError(errRuntime, "the task output is not valid JSON: %v", err)
			}
			return decoded, nil
		case <-task.heartbeat:
			arm()
		case <-expired:
			return nil, newStateError(errHeartbeatTimeout, "the task did not send a heartbeat within HeartbeatSeconds (%d)", *heartbeat)
		case <-ctx.Done():
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
	in.handler.tasks.enqueue(task)

	var result any
	select {
	case worker := <-task.picked:
		in.record(HistoryEvent{Type: evtActivityStarted, ActivityStarted: &activityStartedDetails{WorkerName: worker}})
		result, serr = in.awaitCallback(taskCtx, task, heartbeat)
	case <-taskCtx.Done():
		serr = newStateError(errTimeout, "the activity task was not completed in time")
	}

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
