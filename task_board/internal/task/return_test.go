package task

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Work goes back in the queue only through Return, and it lands with
// the reporter, with the reason in the thread, and on the stream as a move.
func TestReturnHandsTheTaskToItsReporterWithTheReason(t *testing.T) {
	ctx := context.Background()
	fake := newFakeRepo()
	rec := &recorder{}
	svc := &Service{repo: fake, events: rec}
	task := seed(fake, "ABC-1", StatusInProgress)
	task.Reporter = "reporter"
	executor := uuid.New()
	task.Assignee = ptr("executor")

	got, err := svc.Return(ctx, task.ID, ReturnRequest{
		ActorID: executor.String(), Reason: "killed: budget exceeded"})
	require.NoError(t, err)
	require.Equal(t, StatusTodo, got.Status)
	require.Equal(t, task.Reporter, *got.Assignee)

	require.Equal(t, []Event{{Type: EventStatus, TaskID: task.ID, Handle: task.Handle,
		From: StatusInProgress, To: StatusTodo, Assignee: &task.Reporter,
		Note: "Returned to the reporter: killed: budget exceeded", NoteBy: executor.String()}}, rec.events)
}

// A task that is not being worked cannot be returned, and the refusal says
// where it is. A second return of the same task is this case too: answering
// 200 would claim a thread line that was never written.
func TestReturnRefusesATaskNotInProgress(t *testing.T) {
	ctx := context.Background()
	fake := newFakeRepo()
	rec := &recorder{}
	svc := &Service{repo: fake, events: rec}
	task := seed(fake, "ABC-1", StatusQA)

	_, err := svc.Return(ctx, task.ID, ReturnRequest{ActorID: uuid.NewString(), Reason: "r"})
	require.True(t, errors.Is(err, ErrIllegalTransition), err)
	require.Contains(t, err.Error(), "ABC-1 is QA")
	require.Empty(t, rec.events)
}

func TestReturnNeedsAReasonAndAnActor(t *testing.T) {
	svc := &Service{repo: newFakeRepo()}
	_, err := svc.Return(context.Background(), uuid.New(), ReturnRequest{ActorID: uuid.NewString(), Reason: "  "})
	require.True(t, errors.Is(err, ErrInvalid), err)
	_, err = svc.Return(context.Background(), uuid.New(), ReturnRequest{Reason: "r"})
	require.True(t, errors.Is(err, ErrInvalid), err)
}

// The bare move stays closed: without the reassignment the task would sit in
// TODO assigned to whoever dropped it, and a dispatcher would hand it back.
func TestInProgressToTodoIsNotAPlainMove(t *testing.T) {
	require.False(t, CanTransition(StatusInProgress, StatusTodo))
}

func TestPointsBelowOneAreRefused(t *testing.T) {
	zero := 0
	_, err := CreateRequest{ProjectKey: "ABC", Goal: "g", AcceptanceCriteria: "a",
		Reporter: "reporter", Points: &zero}.Validate()
	require.True(t, errors.Is(err, ErrInvalid), err)

	_, err = PatchRequest{Points: &zero}.Validate()
	require.True(t, errors.Is(err, ErrInvalid), err)

	three := 3
	p, err := PatchRequest{Points: &three}.Validate()
	require.NoError(t, err)
	require.Equal(t, 3, *p.points)
}
