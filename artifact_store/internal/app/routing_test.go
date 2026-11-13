package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jurasikasan/units/artifact_store/internal/store"
)

// recordingService answers every call with 200-worthy data and records
// which method the router reached and with what.
type recordingService struct{ calls []string }

func (s *recordingService) Put(ctx context.Context, space, name, contentType, createdBy string, r io.Reader) (*store.File, error) {
	s.calls = append(s.calls, "Put "+space+"/"+name)
	return &store.File{Name: name}, nil
}
func (s *recordingService) Get(ctx context.Context, space, name string) ([]byte, string, error) {
	s.calls = append(s.calls, "Get "+space+"/"+name)
	return []byte("x"), "text/plain", nil
}
func (s *recordingService) List(ctx context.Context, space, prefix string) ([]store.File, error) {
	s.calls = append(s.calls, "List "+space)
	return []store.File{}, nil
}
func (s *recordingService) Delete(ctx context.Context, space, name string) error {
	s.calls = append(s.calls, "Delete "+space+"/"+name)
	return nil
}
func (s *recordingService) GetBySha(ctx context.Context, sha string) ([]byte, string, error) {
	s.calls = append(s.calls, "GetBySha "+sha)
	return []byte("x"), "text/plain", nil
}

// TestShaRouteAndSpaceRoutes: on the module's own router the read by
// sha256 reaches the sha handler, and the space routes still reach theirs
// unchanged (CONTRACT.md section 3; "_" cannot start a space).
func TestShaRouteAndSpaceRoutes(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	for _, tc := range []struct {
		method, path, want string
	}{
		{http.MethodGet, "/v1/_sha256/" + sha, "GetBySha " + sha},
		{http.MethodGet, "/v1/docs/K-1.a", "Get docs/K-1.a"},
		{http.MethodPut, "/v1/docs/K-1.a", "Put docs/K-1.a"},
		{http.MethodDelete, "/v1/docs/K-1.a", "Delete docs/K-1.a"},
		{http.MethodGet, "/v1/docs", "List docs"},
	} {
		svc := &recordingService{}
		w := httptest.NewRecorder()
		NewRouter(svc).ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader("x")))
		if len(svc.calls) != 1 || svc.calls[0] != tc.want {
			t.Errorf("%s %s reached %v, want [%s] (status %d)", tc.method, tc.path, svc.calls, tc.want, w.Code)
		}
	}
}
