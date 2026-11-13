package task

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

// TestListAnswersTasksWithoutTheLongTexts is the size fix for the board list:
// a listed item carries the task without description, acceptance_criteria and
// summary_report — the keys are ABSENT, not empty strings, which is what
// distinguishes "not part of the list" from "part of it and blank". The same
// task on its own route still answers whole, because that is the endpoint a
// reader of the three texts exists for.
func TestListAnswersTasksWithoutTheLongTexts(t *testing.T) {
	fake := newFakeRepo()
	r := mux.NewRouter()
	RegisterRoutes(r, NewHandler(&Service{repo: fake}))

	seeded := seed(fake, "ABC-7", StatusQA)
	seeded.Description = "the long text that runs to pages"
	seeded.AcceptanceCriteria = "what QA checks the result against"
	report := "what was done, why, and where the artefacts are"
	seeded.SummaryReport = &report

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var items []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &items))
	require.Len(t, items, 1)
	for _, key := range []string{"description", "acceptance_criteria", "summary_report"} {
		_, present := items[0][key]
		require.False(t, present, "%s must be absent from a listed item, not empty", key)
	}
	// Every other key of the task stays on the list item, exactly as it is
	// answered by the other routes.
	for _, key := range []string{
		"id", "handle", "project_key", "goal", "status", "reporter",
		"assignee", "points", "parent_task_id", "origin", "discussion",
		"labels", "component", "depends_on", "blocks", "blocked_by",
		"open_subtasks", "created_at", "updated_at", "closed_at",
	} {
		_, present := items[0][key]
		require.True(t, present, "the list item lost a key: %s", key)
	}

	// The one task of the same board, on its own route: whole, and the three
	// texts present with their values.
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/tasks/"+seeded.ID.String(), nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var whole map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &whole))
	require.Equal(t, "the long text that runs to pages", whole["description"])
	require.Equal(t, "what QA checks the result against", whole["acceptance_criteria"])
	require.Equal(t, report, whole["summary_report"])
}

// listedHandles reads the answer back as the spoken names, sorted, so a test
// asserts the set and not the order the fake happens to walk the map in.
func listedHandles(t *testing.T, body []byte) []string {
	t.Helper()
	var items []struct {
		Handle Handle `json:"handle"`
	}
	require.NoError(t, json.Unmarshal(body, &items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Handle.String())
	}
	sort.Strings(out)
	return out
}

// TestOpenKeepsEveryStatusButDoneAndCancelled: open=true is the filter the
// command-line client used to do on its own side, and its vocabulary is the
// closed pair rather than the open five — a new status joins the board open
// without the client changing.
func TestOpenKeepsEveryStatusButDoneAndCancelled(t *testing.T) {
	fake := newFakeRepo()
	r := mux.NewRouter()
	RegisterRoutes(r, NewHandler(&Service{repo: fake}))

	seed(fake, "ABC-1", StatusDraft)
	seed(fake, "ABC-2", StatusTodo)
	seed(fake, "ABC-3", StatusInProgress)
	seed(fake, "ABC-4", StatusQA)
	seed(fake, "ABC-5", StatusUAT)
	seed(fake, "ABC-6", StatusDone)
	seed(fake, "ABC-7", StatusCancelled)

	// Without the filter the list is the whole board.
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, listedHandles(t, rec.Body.Bytes()), 7)

	// open=true keeps every status but DONE and CANCELLED.
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tasks?open=true", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"ABC-1", "ABC-2", "ABC-3", "ABC-4", "ABC-5"},
		listedHandles(t, rec.Body.Bytes()))
}

// TestOpenAndStatusAreRefusedTogether: the same refusal ready has, because
// open=true already says "every status but DONE or CANCELLED" and a status
// list combined with it either ANDs down to empty or repeats it.
func TestOpenAndStatusAreRefusedTogether(t *testing.T) {
	fake := newFakeRepo()
	r := mux.NewRouter()
	RegisterRoutes(r, NewHandler(&Service{repo: fake}))

	seed(fake, "ABC-1", StatusQA)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/tasks?open=true&status=QA", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "open")

	// status alone is still legal, and open with ready is: ready is a subset
	// of open, so the pair does not contradict the way status would.
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tasks?status=QA", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/tasks?open=true&ready=true", nil))
	require.Equal(t, http.StatusOK, rec.Code)
}

// TestOpenRejectsValuesOtherThanTrue: only the literal "true"
// turns the filter on — ?open=false and friends are not "off", they are a
// value the parameter does not take, an empty one included. These are caught before the handler
// reaches the service, which is why svc is nil, the trick
// list_query_test.go uses.
func TestOpenRejectsValuesOtherThanTrue(t *testing.T) {
	h := NewHandler(nil)
	for _, raw := range []string{"yes", "false", "1", "TRUE", ""} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks?open="+raw, nil)
		rec := httptest.NewRecorder()
		h.List(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code, "open=%s", raw)
	}
}
