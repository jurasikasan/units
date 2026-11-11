package subject

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// A subject without a type is not a real subject: the column is TEXT NOT NULL
// whose values are the deployment's own (human, ai, …), so an empty one is a malformed request, not a
// choice to store. Refused before anything touches the repository.
func TestCreateSubject_EmptyTypeRefused(t *testing.T) {
	repo := new(mockSubjectRepository)
	err := NewDefaultSubjectService(repo).CreateSubject(context.Background(),
		&Subject{Name: "X", Nick: "alice", SubjectType: ""})
	assert.ErrorIs(t, err, ErrEmptyType)
	repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
}

func TestCreateSubject_TypePresentSucceeds(t *testing.T) {
	repo := new(mockSubjectRepository)
	repo.On("Create", mock.Anything, mock.MatchedBy(func(s *Subject) bool {
		return s.SubjectType == "ai" && s.Nick == "alice"
	})).Return(nil)
	err := NewDefaultSubjectService(repo).CreateSubject(context.Background(),
		&Subject{Name: "X", Nick: "alice", SubjectType: "ai"})
	assert.NoError(t, err)
	repo.AssertExpectations(t)
}

func TestUpdateSubject_EmptyTypeRefused(t *testing.T) {
	repo := new(mockSubjectRepository)
	err := NewDefaultSubjectService(repo).UpdateSubject(context.Background(),
		&Subject{ID: uuid.New(), Name: "X", Nick: "alice", SubjectType: ""})
	assert.ErrorIs(t, err, ErrEmptyType)
	repo.AssertNotCalled(t, "GetByID", mock.Anything, mock.Anything)
	repo.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
}

// The empty type reaches the client as a 400 with the reason, on both the
// create and the update, instead of being stored or becoming a 500.
func TestEmptyType_StatusCodes(t *testing.T) {
	id := uuid.New().String()
	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"create", http.MethodPost, "/subjects", http.StatusBadRequest},
		{"update", http.MethodPut, "/subjects/" + id, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, h := setup()
			svc.On("CreateSubject", mock.Anything, mock.Anything).Return(ErrEmptyType).Maybe()
			svc.On("UpdateSubject", mock.Anything, mock.Anything).Return(ErrEmptyType).Maybe()
			r := mux.NewRouter()
			r.HandleFunc("/subjects", h.CreateSubject).Methods(http.MethodPost)
			r.HandleFunc("/subjects/{id}", h.UpdateSubject).Methods(http.MethodPut)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"name":"X","nick":"x1"}`)))
			require.Equal(t, tc.want, w.Code, w.Body.String())
		})
	}
}
