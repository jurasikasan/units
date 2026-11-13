package task

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
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
	task.Assignee = ptr("executor")

	got, err := svc.Return(ctx, task.ID, ReturnRequest{
		Actor: "@Watcher", Reason: "killed: budget exceeded"})
	require.NoError(t, err)
	require.Equal(t, StatusTodo, got.Status)
	require.Equal(t, task.Reporter, *got.Assignee)

	require.Equal(t, []Event{{Type: EventStatus, TaskID: task.ID, Handle: task.Handle,
		From: StatusInProgress, To: StatusTodo, Assignee: &task.Reporter,
		Note: "Returned to the reporter: killed: budget exceeded", NoteBy: "watcher"}}, rec.events,
		"the actor is a nick, spelled as the board stores nicks")
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

	_, err := svc.Return(ctx, task.ID, ReturnRequest{Actor: "ann", Reason: "r"})
	require.True(t, errors.Is(err, ErrIllegalTransition), err)
	require.Contains(t, err.Error(), "ABC-1 is QA")
	require.Empty(t, rec.events)
}

func TestReturnNeedsAReasonAndAnActor(t *testing.T) {
	svc := &Service{repo: newFakeRepo()}
	_, err := svc.Return(context.Background(), uuid.New(), ReturnRequest{Actor: "ann", Reason: "  "})
	require.True(t, errors.Is(err, ErrInvalid), err)
	_, err = svc.Return(context.Background(), uuid.New(), ReturnRequest{Reason: "r"})
	require.True(t, errors.Is(err, ErrInvalid), err)
	_, err = svc.Return(context.Background(), uuid.New(), ReturnRequest{Actor: uuid.NewString(), Reason: "r"})
	require.True(t, errors.Is(err, ErrInvalid), "a uuid is not a nick: %v", err)
}

// A caller still sending actor_id learns which field replaced it, rather than
// reading "actor is required" about a field it never heard of.
func TestReturnRefusesActorIDByName(t *testing.T) {
	svc := &Service{repo: newFakeRepo()}
	id := uuid.NewString()
	_, err := svc.Return(context.Background(), uuid.New(), ReturnRequest{ActorID: &id, Reason: "r"})
	require.True(t, errors.Is(err, ErrInvalid), err)
	require.Contains(t, err.Error(), "send actor")
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

// Over HTTP: actor by nick returns the task; the old actor_id body is a 400
// that names the field to send instead.
func TestReturnOverHTTPTakesActorByNick(t *testing.T) {
	fake := newFakeRepo()
	rec := &recorder{}
	r := mux.NewRouter()
	RegisterRoutes(r, NewHandler(&Service{repo: fake, events: rec}))
	task := seed(fake, "ABC-1", StatusInProgress)
	task.Reporter = "ann"
	url := "/api/v1/tasks/" + task.ID.String() + "/return"

	old := httptest.NewRecorder()
	r.ServeHTTP(old, httptest.NewRequest(http.MethodPost, url,
		strings.NewReader(`{"actor_id":"`+uuid.NewString()+`","reason":"stuck"}`)))
	require.Equal(t, http.StatusBadRequest, old.Code, old.Body.String())
	require.Contains(t, old.Body.String(), "send actor")
	require.Empty(t, rec.events)

	ok := httptest.NewRecorder()
	r.ServeHTTP(ok, httptest.NewRequest(http.MethodPost, url,
		strings.NewReader(`{"actor":"bob","reason":"stuck"}`)))
	require.Equal(t, http.StatusOK, ok.Code, ok.Body.String())
	require.Len(t, rec.events, 1)
	require.Equal(t, "bob", rec.events[0].NoteBy)
}
