package stewardship

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
)

type fakeReins struct {
	subs        []Subject
	err         error
	handoverErr error
	gotCaller   string
	gotTo       string
	gotChange   Change
}

func (f *fakeReins) Subject(_ context.Context, caller string, id uuid.UUID) (Subject, error) {
	f.gotCaller = caller
	return Subject{ID: id, Nick: "alice"}, f.err
}

func (f *fakeReins) Change(_ context.Context, caller string, id uuid.UUID, c Change) (Subject, error) {
	f.gotCaller, f.gotChange = caller, c
	return Subject{ID: id, Nick: "alice"}, f.err
}

func (f *fakeReins) MySubjects(_ context.Context, p string) ([]Subject, error) {
	f.gotCaller = p
	return f.subs, f.err
}

func (f *fakeReins) Handover(_ context.Context, caller string, _ uuid.UUID, to string) error {
	f.gotCaller, f.gotTo = caller, to
	return f.handoverErr
}

func serve(f *fakeReins, method, path, principal, body string) *httptest.ResponseRecorder {
	r := mux.NewRouter()
	RegisterRoutes(r, NewHandler(f))
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if principal != "" {
		req.Header.Set(PrincipalHeader, principal)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestMySubjectsNeedsAPrincipal(t *testing.T) {
	rec := serve(&fakeReins{}, http.MethodGet, "/me/subjects", "", "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestMySubjectsAnswers(t *testing.T) {
	f := &fakeReins{subs: []Subject{{ID: uuid.New(), Nick: "alice", SubjectType: TypeHuman}}}
	rec := serve(f, http.MethodGet, "/me/subjects", "alice", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"nick":"alice"`)
	assert.Equal(t, "alice", f.gotCaller)

	rec = serve(&fakeReins{subs: []Subject{}}, http.MethodGet, "/me/subjects", "alice", "")
	assert.Equal(t, "[]", strings.TrimSpace(rec.Body.String()), "none is an empty list, not null")
}

func TestMySubjectsRefusalSaysWhy(t *testing.T) {
	f := &fakeReins{err: &Refusal{Status: 409, Reason: `nick "carol" is taken`}}
	rec := serve(f, http.MethodGet, "/me/subjects", "carol", "")
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), `nick "carol" is taken`)

	rec = serve(&fakeReins{err: errors.New("subjects: connection refused")}, http.MethodGet, "/me/subjects", "carol", "")
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestHandoverAnswers(t *testing.T) {
	id := uuid.New().String()
	cases := []struct {
		name, principal, path, body string
		err                         error
		want                        int
	}{
		{"moved", "alice", "/subjects/" + id + "/handover", `{"to":"bob"}`, nil, http.StatusNoContent},
		{"no principal", "", "/subjects/" + id + "/handover", `{"to":"bob"}`, nil, http.StatusUnauthorized},
		{"bad id", "alice", "/subjects/x/handover", `{"to":"bob"}`, nil, http.StatusBadRequest},
		{"bad body", "alice", "/subjects/" + id + "/handover", `{`, nil, http.StatusBadRequest},
		{"no one to", "alice", "/subjects/" + id + "/handover", `{}`, ErrBadHandover, http.StatusBadRequest},
		{"not a steward", "mallory", "/subjects/" + id + "/handover", `{"to":"bob"}`, ErrNotSteward, http.StatusForbidden},
		{"database", "alice", "/subjects/" + id + "/handover", `{"to":"bob"}`, errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeReins{handoverErr: tc.err}
			rec := serve(f, http.MethodPost, tc.path, tc.principal, tc.body)
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}

func TestSubjectAnswers(t *testing.T) {
	id := uuid.New().String()
	cases := []struct {
		name, principal, path string
		err                   error
		want                  int
	}{
		{"read", "alice", "/subjects/" + id, nil, http.StatusOK},
		{"no principal", "", "/subjects/" + id, nil, http.StatusUnauthorized},
		{"bad id", "alice", "/subjects/x", nil, http.StatusBadRequest},
		{"not a steward", "mallory", "/subjects/" + id, ErrNotSteward, http.StatusForbidden},
		{"gone from subjects", "alice", "/subjects/" + id, ErrNoSubject, http.StatusNotFound},
		{"subjects down", "alice", "/subjects/" + id, errors.New("subjects: connection refused"), http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(&fakeReins{err: tc.err}, http.MethodGet, tc.path, tc.principal, "")
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}

func TestChangeAnswers(t *testing.T) {
	id := uuid.New().String()
	cases := []struct {
		name, principal, body string
		err                   error
		want                  int
	}{
		{"changed", "alice", `{"name":"Alice","preferences":{"ear":"fake-stt","voice":null}}`, nil, http.StatusOK},
		{"no principal", "", `{"name":"Alice"}`, nil, http.StatusUnauthorized},
		{"the nick never changes", "alice", `{"nick":"eve"}`, nil, http.StatusBadRequest},
		{"preferences not an object", "alice", `{"preferences":[1]}`, nil, http.StatusBadRequest},
		{"bad body", "alice", `{`, nil, http.StatusBadRequest},
		{"not a steward", "mallory", `{"name":"Alice"}`, ErrNotSteward, http.StatusForbidden},
		{"gone from subjects", "alice", `{"name":"Alice"}`, ErrNoSubject, http.StatusNotFound},
		{"subjects down", "alice", `{"name":"Alice"}`, errors.New("subjects: 500"), http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeReins{err: tc.err}
			rec := serve(f, http.MethodPatch, "/subjects/"+id, tc.principal, tc.body)
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
	f := &fakeReins{}
	serve(f, http.MethodPatch, "/subjects/"+id, "alice", `{"bio":"","preferences":{"voice":null}}`)
	if assert.NotNil(t, f.gotChange.Bio) {
		assert.Equal(t, "", *f.gotChange.Bio, "an empty bio is a change, not a field left out")
	}
	assert.Nil(t, f.gotChange.Name)
	assert.Equal(t, "null", string(f.gotChange.Preferences["voice"]))
}
