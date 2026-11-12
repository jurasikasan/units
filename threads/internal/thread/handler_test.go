package thread

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

// fakeRepo records what reached storage; the HTTP rules are what is tested
// here, the SQL in repository_test.go against a real Postgres.
type fakeRepo struct {
	appended []*Entry
	page     *Page
	err      error
}

func (f *fakeRepo) CreateThread(context.Context, *Thread) error { return f.err }
func (f *fakeRepo) GetThread(context.Context, uuid.UUID) (*Thread, error) {
	return nil, f.err
}
func (f *fakeRepo) ListThreads(context.Context, Filter) ([]Thread, error) {
	return []Thread{}, f.err
}
func (f *fakeRepo) DeleteThread(context.Context, uuid.UUID) error { return f.err }
func (f *fakeRepo) Append(_ context.Context, e *Entry) error {
	f.appended = append(f.appended, e)
	return f.err
}
func (f *fakeRepo) Entries(_ context.Context, _ uuid.UUID, p Page) ([]Entry, error) {
	f.page = &p
	return []Entry{}, f.err
}

func serve(repo *fakeRepo, method, path, body string) *httptest.ResponseRecorder {
	r := mux.NewRouter()
	RegisterRoutes(r, NewHandler(NewService(repo)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

const tid = "/threads/11111111-1111-1111-1111-111111111111"

func TestAppendStatus(t *testing.T) {
	cases := []struct {
		name string
		body string
		err  error
		want int
	}{
		{"a create", `{"sender":"` + alice.String() + `","kind":"create","content":[{"type":"text","text":"hi"}]}`, nil, 201},
		{"a field the entry does not have", `{"sender":"` + alice.String() + `","kind":"join","role":"user"}`, nil, 400},
		{"a field a block does not have", `{"sender":"` + alice.String() + `","kind":"create","content":[{"type":"text","text":"hi","role":"user"}]}`, nil, 400},
		{"a join with content", `{"sender":"` + alice.String() + `","kind":"join","content":[{"type":"text","text":"hi"}]}`, nil, 400},
		{"no such thread", `{"sender":"` + alice.String() + `","kind":"join"}`, ErrNotFound, 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(&fakeRepo{err: tc.err}, http.MethodPost, tid+"/entries", tc.body)
			require.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}

// A join's content is stored as an empty list, not null: readers range over it.
func TestAppendStoresEmptyContentAsList(t *testing.T) {
	repo := &fakeRepo{}
	rec := serve(repo, http.MethodPost, tid+"/entries", `{"sender":"`+alice.String()+`","kind":"join"}`)
	require.Equal(t, 201, rec.Code)
	require.NotNil(t, repo.appended[0].Content)
	require.Contains(t, rec.Body.String(), `"content":[]`)
}

func TestEntriesPage(t *testing.T) {
	cases := []struct {
		query string
		want  *Page // nil: refused with 400
	}{
		{"", &Page{Limit: 100}},
		{"?after=7&limit=5", &Page{After: 7, Limit: 5}},
		{"?before=7", &Page{Before: 7, Limit: 100}},
		{"?tail=true&limit=20", &Page{Tail: true, Limit: 20}},
		{"?tail=false", &Page{Limit: 100}},
		{"?after=1&before=9", nil},
		{"?tail=true&after=1", nil},
		{"?before=0", nil},
		{"?limit=0", nil},
		{"?limit=1001", nil},
		{"?after=x", nil},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			repo := &fakeRepo{}
			rec := serve(repo, http.MethodGet, tid+"/entries"+tc.query, "")
			if tc.want == nil {
				require.Equal(t, 400, rec.Code, rec.Body.String())
				require.Nil(t, repo.page)
				return
			}
			require.Equal(t, 200, rec.Code, rec.Body.String())
			require.Equal(t, *tc.want, *repo.page)
		})
	}
}

func TestBadIDs(t *testing.T) {
	require.Equal(t, 400, serve(&fakeRepo{}, http.MethodGet, "/threads/nope", "").Code)
	require.Equal(t, 400, serve(&fakeRepo{}, http.MethodGet, "/threads?member=nope", "").Code)
	require.Equal(t, 404, serve(&fakeRepo{err: ErrNotFound}, http.MethodDelete, tid, "").Code)
	require.Equal(t, 204, serve(&fakeRepo{}, http.MethodDelete, tid, "").Code)
}
