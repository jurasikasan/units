package subject

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
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// The rules as decided — lower-case Latin, digits, "-" and "_", 2-32 characters, starting with
// a letter — not from nickPattern.
func TestCreateSubject_NickAlphabet(t *testing.T) {
	tests := []struct {
		nick string
		ok   bool
	}{
		{"alice", true},
		{"bash-executor", true},
		{"agent_2", true},
		{"ab", true},
		{"a" + strings.Repeat("b", 31), true},  // 32
		{"a" + strings.Repeat("b", 32), false}, // 33
		{"a", false},
		{"", false},
		{"Alice", false},  // case makes look-alikes
		{"алиса", false},  // so does Cyrillic
		{"2pac", false},   // starts with a digit
		{"_alice", false}, // starts with a separator
		{"al ice", false},
		{"al.ice", false},
	}
	for _, tc := range tests {
		t.Run(tc.nick, func(t *testing.T) {
			repo := new(mockSubjectRepository)
			if tc.ok {
				repo.On("Create", mock.Anything, mock.Anything).Return(nil)
			}
			err := NewDefaultSubjectService(repo).CreateSubject(context.Background(),
				&Subject{Name: "X", Nick: tc.nick, SubjectType: "human"})
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, ErrInvalidNick)
			}
			repo.AssertExpectations(t) // a refused nick never reaches the database
		})
	}
}

func TestUpdateSubject_NickIsImmutable(t *testing.T) {
	id := uuid.New()
	stored := &Subject{ID: id, Name: "Alice", Nick: "alice", SubjectType: "human"}

	t.Run("a different nick is refused", func(t *testing.T) {
		repo := new(mockSubjectRepository)
		repo.On("GetByID", mock.Anything, id.String()).Return(stored, nil)
		err := NewDefaultSubjectService(repo).UpdateSubject(context.Background(),
			&Subject{ID: id, Name: "Alice", Nick: "alicia", SubjectType: "human"})
		assert.ErrorIs(t, err, ErrNickImmutable)
		repo.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
	})

	for name, nick := range map[string]string{"left out": "", "the same": "alice"} {
		t.Run(name+" keeps the stored nick", func(t *testing.T) {
			repo := new(mockSubjectRepository)
			repo.On("GetByID", mock.Anything, id.String()).Return(stored, nil)
			repo.On("Update", mock.Anything, mock.MatchedBy(func(s *Subject) bool {
				return s.Nick == "alice" && s.Name == "Alice B."
			})).Return(nil)
			err := NewDefaultSubjectService(repo).UpdateSubject(context.Background(),
				&Subject{ID: id, Name: "Alice B.", Nick: nick, SubjectType: "human"})
			assert.NoError(t, err)
			repo.AssertExpectations(t)
		})
	}
}

// The three refusals reach the client as their own status with the reason,
// not as the 500 every service error used to become.
func TestNickRefusals_StatusCodes(t *testing.T) {
	id := uuid.New().String()
	tests := []struct {
		name   string
		method string
		path   string
		err    error
		want   int
	}{
		{"bad alphabet", http.MethodPost, "/subjects", ErrInvalidNick, http.StatusBadRequest},
		{"taken", http.MethodPost, "/subjects", ErrNickTaken, http.StatusConflict},
		{"changed", http.MethodPut, "/subjects/" + id, ErrNickImmutable, http.StatusBadRequest},
		{"other", http.MethodPost, "/subjects", errors.New("db down"), http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, h := setup()
			svc.On("CreateSubject", mock.Anything, mock.Anything).Return(tc.err).Maybe()
			svc.On("UpdateSubject", mock.Anything, mock.Anything).Return(tc.err).Maybe()
			r := mux.NewRouter()
			r.HandleFunc("/subjects", h.CreateSubject).Methods(http.MethodPost)
			r.HandleFunc("/subjects/{id}", h.UpdateSubject).Methods(http.MethodPut)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"name":"X","nick":"x1"}`)))
			require.Equal(t, tc.want, w.Code, w.Body.String())
		})
	}
}
