package task

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

// TestRouteOrdering pins a mistake that is found only by calling the endpoint:
// gorilla/mux matches in registration order, so a literal path registered
// after `/tasks/{id}` is unreachable — the id handler takes it and answers 400
// "invalid task id", with a green build.
//
// The assertion is on the matched path template rather than on the order of
// lines in a file, because the order of lines is not the bug — what a request
// resolves to is.
func TestRouteOrdering(t *testing.T) {
	r := mux.NewRouter()
	RegisterRoutes(r, NewHandler(nil))

	cases := []struct {
		name, method, path, want string
	}{
		{"collection post", http.MethodPost, "/api/v1/tasks", "/api/v1/tasks"},
		{"collection get", http.MethodGet, "/api/v1/tasks", "/api/v1/tasks"},
		{
			"single task", http.MethodGet,
			"/api/v1/tasks/2a1f8c6e-0000-4000-8000-000000000000",
			"/api/v1/tasks/{id}",
		},
		{
			"status sub-route", http.MethodPut,
			"/api/v1/tasks/2a1f8c6e-0000-4000-8000-000000000000/status",
			"/api/v1/tasks/{id}/status",
		},
		{
			"dependencies sub-route", http.MethodPut,
			"/api/v1/tasks/2a1f8c6e-0000-4000-8000-000000000000/dependencies",
			"/api/v1/tasks/{id}/dependencies",
		},
		{
			"assignee", http.MethodPut,
			"/api/v1/tasks/2a1f8c6e-0000-4000-8000-000000000000/assignee",
			"/api/v1/tasks/{id}/assignee",
		},
		{
			"summary", http.MethodPut,
			"/api/v1/tasks/2a1f8c6e-0000-4000-8000-000000000000/summary",
			"/api/v1/tasks/{id}/summary",
		},
		{
			"delete task", http.MethodDelete,
			"/api/v1/tasks/2a1f8c6e-0000-4000-8000-000000000000",
			"/api/v1/tasks/{id}",
		},
		// The spoken name resolves through the same route as the uuid. If it
		// ever gets its own, this case is what notices.
		{"by handle", http.MethodGet, "/api/v1/tasks/ABC-412", "/api/v1/tasks/{id}"},
		{"projects post", http.MethodPost, "/api/v1/projects", "/api/v1/projects"},
		{"projects get", http.MethodGet, "/api/v1/projects", "/api/v1/projects"},
		{
			"delete project", http.MethodDelete,
			"/api/v1/projects/ABC", "/api/v1/projects/{key}",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var match mux.RouteMatch
			req := httptest.NewRequest(tc.method, tc.path, nil)
			require.True(t, r.Match(req, &match), "no route matched %s %s", tc.method, tc.path)

			got, err := match.Route.GetPathTemplate()
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestLiteralsBeforeWildcard fails the moment someone adds a literal sub-path
// after /tasks/{id}, which is the only way to reintroduce that bug. Without it
// the test above keeps passing while the new route is dead.
func TestLiteralsBeforeWildcard(t *testing.T) {
	r := mux.NewRouter()
	RegisterRoutes(r, NewHandler(nil))

	seenWildcard := false
	err := r.Walk(func(route *mux.Route, _ *mux.Router, _ []*mux.Route) error {
		tmpl, err := route.GetPathTemplate()
		if err != nil {
			return err
		}
		if tmpl == "/api/v1/tasks/{id}" {
			seenWildcard = true
			return nil
		}
		if seenWildcard && !containsBrace(tmpl) {
			t.Errorf("literal route %q is registered after /api/v1/tasks/{id} "+
				"and is therefore unreachable", tmpl)
		}
		return nil
	})
	require.NoError(t, err)
	require.True(t, seenWildcard, "/api/v1/tasks/{id} is no longer registered — "+
		"this test is not checking anything")
}

func containsBrace(s string) bool {
	for _, c := range s {
		if c == '{' {
			return true
		}
	}
	return false
}
