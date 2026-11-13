package task

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestListQueryParamValidation covers the query-string parsing of GET /tasks: a UUID as assignee, an unknown status in the list,
// and an unknown order word. All three are caught before the handler ever
// reaches the service, which is why svc is nil here — the same trick
// routes_test.go uses for routes that never run a handler body.
func TestListQueryParamValidation(t *testing.T) {
	h := NewHandler(nil)
	cases := []struct{ name, query string }{
		{"uuid as assignee", "assignee=2a1f8c6e-0000-4000-8000-000000000001"},
		{"unknown status in list", "status=QA,REVIEW"},
		{"unknown order", "order=handle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks?"+tc.query, nil)
			rec := httptest.NewRecorder()
			h.List(rec, req)
			require.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

// TestParseSearch pins which way the search box goes. The case that matters is
// the lowercase handle: typed by hand it must still find the task rather than
// fall through to a goal search that finds nothing.
func TestParseSearch(t *testing.T) {
	cases := []struct {
		raw      string
		handle   *Handle
		goalLike string
	}{
		{"", nil, ""},
		{"   ", nil, ""},
		{"XYZ-45", &Handle{Project: "XYZ", Number: 45}, ""},
		{" xyz-45 ", &Handle{Project: "XYZ", Number: 45}, ""},
		{"287", &Handle{Number: 287}, ""},
		{"0", nil, "0"},
		{"OCR", nil, "OCR"},
		{"XYZ-", nil, "XYZ-"},
		{"pdftotext -layout", nil, "pdftotext -layout"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			h, like := parseSearch(tc.raw)
			require.Equal(t, tc.handle, h)
			require.Equal(t, tc.goalLike, like)
		})
	}
}

func TestEscapeLike(t *testing.T) {
	require.Equal(t, `100\% a\_b c\\d`, escapeLike(`100% a_b c\d`))
}
