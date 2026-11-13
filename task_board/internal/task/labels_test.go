package task

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

// The normalization rule: trim, empties
// dropped, duplicates collapsed, order as sent. The cases are the rule, not
// the implementation's habits.
func TestNormalizeLabels(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"absent", nil, []string{}},
		{"empty", []string{}, []string{}},
		{"only blanks", []string{"  "}, []string{}},
		{"trim, drop empties, collapse duplicates",
			[]string{" bug ", "bug", "", "   ", "ui "}, []string{"bug", "ui"}},
		{"duplicates keep first order", []string{"a", "b", "a"}, []string{"a", "b"}},
		{"different spellings are different labels",
			[]string{"Bug", "bug"}, []string{"Bug", "bug"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, normalizeLabels(tc.in))
		})
	}
}

// validCreate is the shortest request that passes validation, so a test can
// vary one field and mean it.
func validCreate(labels []string) CreateRequest {
	return CreateRequest{
		ProjectKey:         "ABC",
		Goal:               "a goal",
		AcceptanceCriteria: "what QA checks",
		Reporter:           "reporter",
		Labels:             labels,
	}
}

// Create takes labels and stores them normalized: the row the caller gets back
// holds the rule's output, not the request's spelling.
func TestCreateNormalizesLabels(t *testing.T) {
	fake := newFakeRepo()
	svc := &Service{repo: fake}

	t0, err := svc.Create(context.Background(),
		validCreate([]string{" bug ", "bug", "", "ui "}))
	require.NoError(t, err)
	require.Equal(t, []string{"bug", "ui"}, t0.Labels,
		"trim, no empties, no duplicates, order as sent")
	require.Equal(t, []string{"bug", "ui"}, fake.tasks[t0.ID].Labels,
		"the stored row, not just the answer, is normalized")

	none, err := svc.Create(context.Background(), validCreate(nil))
	require.NoError(t, err)
	require.Equal(t, []string{}, none.Labels, "no labels is an ordinary task: [], not null")
}

// PATCH with labels replaces the whole set — the previous labels are gone, not
// merged — and the replacement goes through the same normalization.
func TestPatchReplacesLabels(t *testing.T) {
	fake := newFakeRepo()
	svc := &Service{repo: fake}
	t0 := seed(fake, "ABC-1", StatusTodo)
	t0.Labels = []string{"a", "b"}

	next := []string{" c ", "c", "", "d "}
	updated, err := svc.Patch(context.Background(), t0.ID, PatchRequest{Labels: &next})
	require.NoError(t, err)
	require.Equal(t, []string{"c", "d"}, updated.Labels,
		"replacement, not merge: a and b are gone, c and d are normalized")
}

// PATCH without the field leaves the labels exactly as they are. This is the
// pointer-vs-value half of the contract: "not sent" and "sent as empty" are
// different requests, and only the second clears.
func TestPatchWithoutLabelsUntouched(t *testing.T) {
	fake := newFakeRepo()
	svc := &Service{repo: fake}
	t0 := seed(fake, "ABC-1", StatusTodo)
	t0.Labels = []string{"a", "b"}

	goal := "a new goal"
	updated, err := svc.Patch(context.Background(), t0.ID, PatchRequest{Goal: &goal})
	require.NoError(t, err)
	require.Equal(t, "a new goal", updated.Goal)
	require.Equal(t, []string{"a", "b"}, updated.Labels,
		"a patch that does not name labels must not change them")
}

// "Sent as empty" is the clear request — there is no other endpoint for
// removing every label — and a patch that is only labels is still a patch, not
// an empty one.
func TestPatchClearsLabelsWhenSentEmpty(t *testing.T) {
	fake := newFakeRepo()
	svc := &Service{repo: fake}
	t0 := seed(fake, "ABC-1", StatusTodo)
	t0.Labels = []string{"a", "b"}

	empty := []string{}
	updated, err := svc.Patch(context.Background(), t0.ID, PatchRequest{Labels: &empty})
	require.NoError(t, err, "labels-only is a patch; [] clears")
	require.Equal(t, []string{}, updated.Labels)
}

// GET answers [] for a task with no labels — never null. The shape is what
// clients branch on, so it is asserted in the wire bytes, not in a struct
// that a decoder would have to fill in for us.
func TestGetAnswersEmptyLabelsNotNull(t *testing.T) {
	fake := newFakeRepo()
	h := NewHandler(&Service{repo: fake})
	r := mux.NewRouter()
	RegisterRoutes(r, h)

	t0 := seed(fake, "ABC-1", StatusTodo)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+t0.ID.String(), nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"labels":[]`,
		"a task with no labels answers [], not null")
	require.NotContains(t, rec.Body.String(), `"labels":null`)
}

// ?label= narrows to tasks carrying the label; repeated parameters AND — every
// named label must be on the task — and a label nobody carries answers empty.
func TestListLabelFilter(t *testing.T) {
	fake := newFakeRepo()
	h := NewHandler(&Service{repo: fake})
	r := mux.NewRouter()
	RegisterRoutes(r, h)

	both := seed(fake, "ABC-1", StatusTodo)
	both.Labels = []string{"bug", "ui"}
	one := seed(fake, "ABC-2", StatusTodo)
	one.Labels = []string{"bug"}
	other := seed(fake, "ABC-3", StatusTodo)
	other.Labels = []string{"ui"}
	seed(fake, "ABC-4", StatusTodo) // no labels

	cases := []struct {
		query string
		want  []string
	}{
		{"label=bug", []string{"ABC-1", "ABC-2"}},
		{"label=bug&label=ui", []string{"ABC-1"}},
		{"label=bug&label=ui&label=bug", []string{"ABC-1"}},
		{"label=nobody", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks?"+tc.query, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code)

			var got []Task
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			handles := make([]string, 0, len(got))
			for _, task := range got {
				handles = append(handles, task.Handle.String())
			}
			require.ElementsMatch(t, tc.want, handles, "the filter must mean what the query says")
		})
	}
}

// ?label= present but blank is a filter the caller did not mean to send; it
// is refused rather than read as "every task", which is what ?label= left off.
func TestListBlankLabelIsRefused(t *testing.T) {
	h := NewHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks?label=", nil)
	rec := httptest.NewRecorder()
	h.List(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// /api/v1/labels: the project's existing labels with their open-task counts,
// busiest first. A label that exists only on a closed task still exists — it
// is the project's vocabulary — and answers with 0, which is what the count
// is for.
func TestListLabels(t *testing.T) {
	fake := newFakeRepo()
	h := NewHandler(&Service{repo: fake})
	r := mux.NewRouter()
	RegisterRoutes(r, h)

	open1 := seed(fake, "ABC-1", StatusTodo)
	open1.Labels = []string{"bug", "ui"}
	open2 := seed(fake, "ABC-2", StatusInProgress)
	open2.Labels = []string{"bug"}
	closed := seed(fake, "ABC-3", StatusDone)
	closed.Labels = []string{"bug", "legacy"}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/labels?project=ABC", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var got []LabelCount
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, []LabelCount{
		{Label: "bug", OpenTasks: 2},
		{Label: "ui", OpenTasks: 1},
		{Label: "legacy", OpenTasks: 0},
	}, got, "existing labels, open counts, descending")
}

// The same endpoint, empty project: [] and not null — the list is the answer,
// and an empty list is an answer.
func TestListLabelsEmptyProject(t *testing.T) {
	fake := newFakeRepo()
	h := NewHandler(&Service{repo: fake})
	r := mux.NewRouter()
	RegisterRoutes(r, h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/labels?project=ABC", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "[]", strings.TrimSpace(rec.Body.String()))
}

// A label lives inside one project's numbering; asking without a project is a
// request with no answer, not a cross-project one.
func TestListLabelsRequiresProject(t *testing.T) {
	fake := newFakeRepo()
	h := NewHandler(&Service{repo: fake})

	cases := []string{
		"/api/v1/labels",
		"/api/v1/labels?project=",
		"/api/v1/labels?project=1",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			h.ListLabels(rec, req)
			require.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}
