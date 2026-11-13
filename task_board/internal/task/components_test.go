package task

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

// The component is a project property: the project
// holds the list, the task holds one of its values. The expected values in
// these tests are the rule's: 400 with the allowed list for a value outside it,
// "" clearing on PATCH, ?component= filtering, and 409 with the task count
// when a list change removes a value still in use.

// componentBoard seeds project ABC with the component list api, web.
func componentBoard() *fakeRepo {
	fake := newFakeRepo()
	seedProject(fake, "ABC", "api", "web")
	return fake
}

func componentRouter(t *testing.T, fake *fakeRepo) *mux.Router {
	t.Helper()
	r := mux.NewRouter()
	RegisterRoutes(r, NewHandler(&Service{repo: fake}))
	return r
}

func postComponentTask(t *testing.T, r *mux.Router, component string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"project_key":         "ABC",
		"goal":                "a goal",
		"acceptance_criteria": "what QA checks",
		"reporter":            "reporter",
		"component":           component,
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks",
		strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func patchComponent(t *testing.T, r *mux.Router, id uuid.UUID, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/tasks/"+id.String(),
		strings.NewReader(string(raw)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func putComponents(t *testing.T, r *mux.Router, key string, components []string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(ComponentsRequest{Components: components})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPut,
		"/api/v1/projects/"+key+"/components", strings.NewReader(string(raw)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// A component outside the project's list is refused, and the refusal names
// the list: the caller needs to know what is allowed, not just that this
// value is not.
func TestCreateComponentOutsideProjectListIsRefused(t *testing.T) {
	fake := componentBoard()
	r := componentRouter(t, fake)

	rec := postComponentTask(t, r, "mobile")
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "api", "the refusal names the allowed values")
	require.Contains(t, rec.Body.String(), "web")
	require.Empty(t, fake.tasks, "a refused task is not a row")
}

// A value from the list is accepted, and the stored row — not only the answer
// — holds it, trimmed the way the service stores everything it normalises.
func TestCreateComponentInProjectListIsAccepted(t *testing.T) {
	fake := componentBoard()
	r := componentRouter(t, fake)

	rec := postComponentTask(t, r, " api ")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"component":"api"`)
	require.Len(t, fake.tasks, 1)
	for _, stored := range fake.tasks {
		require.NotNil(t, stored.Component)
		require.Equal(t, "api", *stored.Component)
	}
}

// No component is a state, and it is answered null — the task spanning several
// components, recorded in labels.
func TestCreateWithoutComponentAnswersNull(t *testing.T) {
	fake := componentBoard()
	r := componentRouter(t, fake)

	rec := postComponentTask(t, r, "")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"component":null`)
	for _, stored := range fake.tasks {
		require.Nil(t, stored.Component)
	}
}

// PATCH: a value from the list sets it, a value outside is the same 400, ""
// clears to null, and not sending the field leaves it alone — the pointer is
// what separates the two.
func TestPatchComponentRules(t *testing.T) {
	fake := componentBoard()
	r := componentRouter(t, fake)
	t0 := seed(fake, "ABC-1", StatusTodo)

	rec := patchComponent(t, r, t0.ID, map[string]any{"component": "web"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"component":"web"`)
	require.Equal(t, "web", *fake.tasks[t0.ID].Component)

	rec = patchComponent(t, r, t0.ID, map[string]any{"component": "cli"})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "api")
	require.Contains(t, rec.Body.String(), "web")
	require.Equal(t, "web", *fake.tasks[t0.ID].Component, "a refused patch changes nothing")

	rec = patchComponent(t, r, t0.ID, map[string]any{"component": ""})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"component":null`)
	require.Nil(t, fake.tasks[t0.ID].Component)

	fake.tasks[t0.ID].Component = strPtr("web")
	rec = patchComponent(t, r, t0.ID, map[string]any{"goal": "a new goal"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "web", *fake.tasks[t0.ID].Component, "not sent is not cleared")
}

// ?component= is an equality filter that reaches ListFilter; tasks without a
// component are the ones it drops, which is the point of it.
func TestListComponentFilter(t *testing.T) {
	fake := componentBoard()
	r := componentRouter(t, fake)
	with := seed(fake, "ABC-1", StatusTodo)
	seed(fake, "ABC-2", StatusTodo) // no component: the filter must drop it
	with.Component = strPtr("api")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks?component=api", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "ABC-1")
	require.NotContains(t, rec.Body.String(), "ABC-2",
		"a task without a component is not a task with a different one")

	// And the filter is the ListFilter field, not a re-parsed query: the
	// service level takes the same value and narrows the same way.
	svc := &Service{repo: fake}
	list, err := svc.List(context.Background(), ListFilter{Component: "api"})
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, with.ID, list[0].ID)
}

// A blank component is a request, and gets the same refusal a blank label
// gets: the filter that would match everything is the filter not sent.
func TestListBlankComponentIsRefused(t *testing.T) {
	r := componentRouter(t, componentBoard())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks?component=", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// Removing a component the project's tasks still hold is a 409 that names the
// count — the number of rows that would point at a value the registry no
// longer offers — and the list is left as it was.
func TestSetComponentsRefusesRemovingAnInUseValue(t *testing.T) {
	fake := componentBoard()
	r := componentRouter(t, fake)
	a := seed(fake, "ABC-1", StatusTodo)
	b := seed(fake, "ABC-2", StatusTodo)
	a.Component = strPtr("api")
	b.Component = strPtr("api")

	rec := putComponents(t, r, "ABC", []string{"web"})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "2", "the refusal is the count of such tasks")
	require.Equal(t, []string{"api", "web"}, fake.projects["ABC"].Components,
		"a refused change is no change")
}

// The replacement goes through the same normalization a labels list does, and
// the reader answers the list — [] when the project has none, never null,
// because the column is NOT NULL DEFAULT '{}'.
func TestSetComponentsReplacesAndNormalizes(t *testing.T) {
	fake := componentBoard()
	r := componentRouter(t, fake)

	rec := putComponents(t, r, "ABC", []string{" api ", "api", "", "mobile"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"api", "mobile"}, fake.projects["ABC"].Components)
	require.Contains(t, rec.Body.String(), `"components":["api","mobile"]`)

	bare := newFakeRepo()
	seedProject(bare, "XYZ")
	rec = putComponents(t, r, "XYZ", nil) // a different project: untouched
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	rec = httptest.NewRecorder()
	componentRouter(t, bare).ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"components":[]`,
		"a project without components is [] and never null")
}

// A task answers its component by either address, and a project's answer
// carries the list.
func TestGetTaskAndProjectCarryComponents(t *testing.T) {
	fake := componentBoard()
	r := componentRouter(t, fake)
	t0 := seed(fake, "ABC-1", StatusTodo)
	t0.Component = strPtr("web")

	for _, path := range []string{"/api/v1/tasks/" + t0.ID.String(), "/api/v1/tasks/ABC-1"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, path+" "+rec.Body.String())
		require.Contains(t, rec.Body.String(), `"component":"web"`, path)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"components":["api","web"]`)
}

func strPtr(s string) *string { return &s }
