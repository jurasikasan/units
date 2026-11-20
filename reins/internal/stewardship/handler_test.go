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
