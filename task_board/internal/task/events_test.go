package task

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recorder is the stream as the service sees it: every Publish, in order.
type recorder struct {
	events []Event
	err    error
}

func (r *recorder) Publish(_ context.Context, e Event) error {
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, e)
	return nil
}

func ptr(s string) *string { return &s }

// The claim a consumer makes: two dispatchers woken by the same event (or one
// woken twice by a redelivery) both ask for TODO → IN_PROGRESS. Without from the
// second gets the idempotent 200 and believes it has the task; with from it is
// told the task has gone.
func TestSetStatusFromMakesASecondClaimFail(t *testing.T) {
	ctx := context.Background()
	fake := newFakeRepo()
	rec := &recorder{}
	svc := &Service{repo: fake, events: rec}
	task := seed(fake, "ABC-1", StatusTodo)
	// The claim is about two dispatchers racing on the same task, not about the
	// assignee gate: give the task an owner so the first claim can go through.
	who := "dispatcher"
	fake.tasks[task.ID].Assignee = &who

	claim := StatusRequest{Status: StatusInProgress, From: ptr(StatusTodo)}
	got, err := svc.SetStatus(ctx, task.ID, claim)
	require.NoError(t, err)
	require.Equal(t, StatusInProgress, got.Status)

	_, err = svc.SetStatus(ctx, task.ID, claim)
	require.ErrorIs(t, err, ErrIllegalTransition)
	require.Contains(t, err.Error(), "ABC-1 is IN_PROGRESS, not TODO")

	// Without from, the same request stays the no-op 200 it was before.
	got, err = svc.SetStatus(ctx, task.ID, StatusRequest{Status: StatusInProgress})
	require.NoError(t, err)
	require.Equal(t, StatusInProgress, got.Status)

	require.Len(t, rec.events, 1, "only the move that happened is published")
}

// from that disagrees with the task refuses even a move that would be legal from
// where the task actually is, and writes nothing.
func TestSetStatusFromMismatchWritesNothing(t *testing.T) {
	ctx := context.Background()
	fake := newFakeRepo()
	rec := &recorder{}
	svc := &Service{repo: fake, events: rec}
	task := seed(fake, "ABC-1", StatusTodo)

	_, err := svc.SetStatus(ctx, task.ID, StatusRequest{Status: StatusCancelled, From: ptr(StatusQA)})
	require.ErrorIs(t, err, ErrIllegalTransition)
	require.Equal(t, StatusTodo, fake.tasks[task.ID].Status)
	require.Empty(t, rec.events)
}

func TestSetStatusFromMustBeAStatus(t *testing.T) {
	fake := newFakeRepo()
	svc := &Service{repo: fake}
	task := seed(fake, "ABC-1", StatusTodo)
	for _, from := range []string{"", "REVIEW"} {
		_, err := svc.SetStatus(context.Background(), task.ID,
			StatusRequest{Status: StatusInProgress, From: ptr(from)})
		require.ErrorIs(t, err, ErrInvalid, "from=%q", from)
	}
	require.Equal(t, StatusTodo, fake.tasks[task.ID].Status)
}

func TestSetStatusFromOverHTTPAnswers409(t *testing.T) {
	fake := newFakeRepo()
	r := mux.NewRouter()
	RegisterRoutes(r, NewHandler(&Service{repo: fake}))
	task := seed(fake, "ABC-1", StatusInProgress)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/tasks/"+task.ID.String()+"/status",
		strings.NewReader(`{"status":"IN_PROGRESS","from":"TODO"}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "not TODO")
}

func TestStatusEventCarriesFromToAndAssignee(t *testing.T) {
	ctx := context.Background()
	fake := newFakeRepo()
	rec := &recorder{}
	svc := &Service{repo: fake, events: rec}
	task := seed(fake, "ABC-1", StatusDraft)
	who := "alisa"
	fake.tasks[task.ID].Assignee = &who

	_, err := svc.SetStatus(ctx, task.ID, StatusRequest{Status: StatusTodo})
	require.NoError(t, err)
	require.Equal(t, []Event{{Type: EventStatus, TaskID: task.ID, Handle: task.Handle,
		From: StatusDraft, To: StatusTodo, Assignee: &who}}, rec.events)
}

// A move that did not happen — refused by the flow — says nothing.
func TestRefusedMoveIsNotPublished(t *testing.T) {
	fake := newFakeRepo()
	rec := &recorder{}
	svc := &Service{repo: fake, events: rec}
	task := seed(fake, "ABC-1", StatusTodo)

	_, err := svc.SetStatus(context.Background(), task.ID, StatusRequest{Status: StatusDone})
	require.ErrorIs(t, err, ErrIllegalTransition)
	require.Empty(t, rec.events)
}

func TestAssigneeEventCarriesCurrentStatus(t *testing.T) {
	ctx := context.Background()
	fake := newFakeRepo()
	rec := &recorder{}
	svc := &Service{repo: fake, events: rec}
	task := seed(fake, "ABC-1", StatusTodo)
	who := "alisa"

	_, err := svc.SetAssignee(ctx, task.ID, AssigneeRequest{Assignee: ptr(who)})
	require.NoError(t, err)
	_, err = svc.SetAssignee(ctx, task.ID, AssigneeRequest{})
	require.NoError(t, err)

	require.Equal(t, []Event{
		{Type: EventAssignee, TaskID: task.ID, Handle: task.Handle, From: StatusTodo, To: StatusTodo, Assignee: &who},
		{Type: EventAssignee, TaskID: task.ID, Handle: task.Handle, From: StatusTodo, To: StatusTodo},
	}, rec.events)
}

// The write has happened by the time the event goes out; a stream that is down
// must not turn a done move into an error the caller would retry.
func TestPublishFailureDoesNotFailTheMove(t *testing.T) {
	fake := newFakeRepo()
	svc := &Service{repo: fake, events: &recorder{err: errors.New("redis down")}}
	task := seed(fake, "ABC-1", StatusTodo)
	// The test is about a dead publisher, not the assignee gate: the task has
	// an owner, so the move itself is legal.
	who := "alisa"
	fake.tasks[task.ID].Assignee = &who

	got, err := svc.SetStatus(context.Background(), task.ID, StatusRequest{Status: StatusInProgress})
	require.NoError(t, err)
	require.Equal(t, StatusInProgress, got.Status)
}

// The real publisher, against a Redis: the entry lands on board_events in the
// CloudEvents envelope of contracts/events.md — the task's id as the subject,
// the rest in data — and an unassigned task has "assignee": null.
func TestRedisPublisherWritesTheEntry(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	h, err := ParseHandle("ABC-311")
	require.NoError(t, err)
	id, who, by := uuid.New(), "alisa", uuid.New()
	pub := NewRedisPublisher(rdb)
	ctx := context.Background()
	require.NoError(t, pub.Publish(ctx, Event{Type: EventStatus, TaskID: id, Handle: h,
		From: StatusInProgress, To: StatusTodo, Assignee: &who,
		Note: "Returned to the reporter: stuck", NoteBy: by.String()}))
	require.NoError(t, pub.Publish(ctx, Event{Type: EventAssignee, TaskID: id, Handle: h,
		From: StatusTodo, To: StatusTodo}))

	msgs, err := rdb.XRange(ctx, "board_events", "-", "+").Result()
	require.NoError(t, err)
	require.Len(t, msgs, 2)

	first := msgs[0].Values
	assert.Equal(t, "1.0", first["ce_specversion"])
	assert.Equal(t, "/task_board", first["ce_source"])
	assert.Equal(t, "task_board.task.status_changed", first["ce_type"])
	assert.Equal(t, id.String(), first["ce_subject"])
	assert.Equal(t, "application/json", first["ce_datacontenttype"])
	assert.NotEmpty(t, first["ce_id"])
	assert.NotEmpty(t, first["ce_time"])
	assert.JSONEq(t, `{"handle":"ABC-311","from":"IN_PROGRESS","to":"TODO","assignee":"alisa",
		"note":"Returned to the reporter: stuck","note_by":"`+by.String()+`"}`, first["data"].(string))

	second := msgs[1].Values
	assert.Equal(t, "task_board.task.assignee_changed", second["ce_type"])
	assert.JSONEq(t, `{"handle":"ABC-311","from":"TODO","to":"TODO","assignee":null}`, second["data"].(string))
}

// A note the board says itself carries no note_by: the consumer attributes it.
func TestBoardsOwnNoteHasNoNoteBy(t *testing.T) {
	h, err := ParseHandle("ABC-2")
	require.NoError(t, err)
	d := Event{Type: EventStatus, Handle: h, From: StatusInProgress, To: StatusTodo,
		Note: "Parked: waits for ABC-1; resumes when it closes"}.Data()
	assert.Equal(t, "Parked: waits for ABC-1; resumes when it closes", d["note"])
	assert.NotContains(t, d, "note_by")
}
