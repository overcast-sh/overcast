package scheduler

// Concurrency coverage for the schedule record.
//
// A schedule is stored as one blob, so UpdateSchedule reads the whole record,
// builds the replacement and writes the whole record back. Anything that
// changes the record between that read and that write is discarded — including
// a DeleteSchedule the caller was already told had succeeded.

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/state"
)

// pausingStore holds the first read of a schedule record open until it is
// released, so a second caller can be driven into the window between another
// operation's read and its write.
type pausingStore struct {
	state.Store
	armed   chan struct{} // closed once the paused read is in the window
	release chan struct{}
	done    bool // guarded by the store's single-reader use in these tests
}

func (p *pausingStore) Get(ctx context.Context, namespace, key string) (string, bool, error) {
	value, found, err := p.Store.Get(ctx, namespace, key)
	if namespace == nsSchedules && !p.done {
		p.done = true
		close(p.armed)
		<-p.release
	}
	return value, found, err
}

// newPausedService returns a service over a pausing store, holding one schedule
// named "s1" in the default group.
//
// The seed schedule is created against a plain store, before the pausing
// wrapper goes on: CreateSchedule now reads a schedule's own record to check
// for a duplicate name (see typed_logic.go), which is itself a Get against
// nsSchedules. Installing the pausing store first would arm and block on that
// existence check instead of on the read this test means to catch — the
// update/delete race below — hanging setup itself rather than exercising it.
func newPausedService(t *testing.T) (*Service, *pausingStore) {
	t.Helper()
	mem := state.NewMemoryStore()
	clk := clock.NewMock()
	clk.Set(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	s := New(&config.Config{Region: "us-east-1", AccountID: "000000000000"}, mem, zap.NewNop(), clk)

	if _, aerr := s.createScheduleTyped(context.Background(), &createScheduleRequest{
		Name:               "s1",
		ScheduleExpression: "rate(5 minutes)",
		FlexibleTimeWindow: flexibleTimeWindow{Mode: "OFF"},
		Target:             scheduleTarget{Arn: "arn:aws:sqs:us-east-1:000000000000:q"},
	}); aerr != nil {
		t.Fatalf("create schedule: %v", aerr)
	}

	st := &pausingStore{
		Store:   mem,
		armed:   make(chan struct{}),
		release: make(chan struct{}),
	}
	s.store = st
	return s, st
}

func replacementFor(name string) *updateScheduleRequest {
	return &updateScheduleRequest{
		Name:               name,
		ScheduleExpression: "rate(15 minutes)",
		FlexibleTimeWindow: flexibleTimeWindow{Mode: "OFF"},
		Target:             scheduleTarget{Arn: "arn:aws:sqs:us-east-1:000000000000:q"},
	}
}

func TestUpdateSchedule_doesNotResurrectAConcurrentlyDeletedSchedule(t *testing.T) {
	// Given: an UpdateSchedule that has read the record and not yet written it
	s, st := newPausedService(t)
	ctx := context.Background()

	updated := make(chan *protocol.AWSError, 1)
	go func() {
		_, aerr := s.updateScheduleTyped(ctx, replacementFor("s1"))
		updated <- aerr
	}()
	select {
	case <-st.armed:
	case <-time.After(5 * time.Second):
		t.Fatal("the update never reached the store")
	}

	// When: the schedule is deleted while that update is in its window
	deleted := make(chan *protocol.AWSError, 1)
	go func() {
		_, aerr := s.deleteScheduleTyped(ctx, &deleteScheduleRequest{Name: "s1"})
		deleted <- aerr
	}()
	time.Sleep(50 * time.Millisecond) // let the delete get as far as it can
	close(st.release)

	if aerr := waitForResult(t, updated, "update"); aerr != nil {
		t.Fatalf("update: %v", aerr)
	}
	if aerr := waitForResult(t, deleted, "delete"); aerr != nil {
		t.Fatalf("delete: %v", aerr)
	}

	// Then: the schedule stays deleted. Without the record lock the update's
	// write lands after the delete and puts it back — still stored, still
	// firing, and reported as deleted.
	_, found, aerr := s.loadSchedule(ctx, "us-east-1", defaultGroup, "s1")
	if aerr != nil {
		t.Fatalf("load schedule: %v", aerr)
	}
	if found {
		t.Fatal("the deleted schedule is back: UpdateSchedule wrote over a DeleteSchedule that had already succeeded")
	}
}

func waitForResult(t *testing.T, ch <-chan *protocol.AWSError, what string) *protocol.AWSError {
	t.Helper()
	select {
	case aerr := <-ch:
		return aerr
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not finish", what)
		return nil
	}
}
