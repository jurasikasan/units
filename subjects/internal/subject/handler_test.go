package subject

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type mockSubjectService struct {
	mock.Mock
}

func (m *mockSubjectService) CreateSubject(ctx context.Context, s *Subject) error {
	args := m.Called(ctx, s)
	return args.Error(0)
}

func (m *mockSubjectService) GetAllSubjects(ctx context.Context) ([]Subject, error) {
	args := m.Called(ctx)
	return args.Get(0).([]Subject), args.Error(1)
}

func (m *mockSubjectService) GetSubjectByID(ctx context.Context, id string) (*Subject, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*Subject), args.Error(1)
}

func (m *mockSubjectService) UpdateSubject(ctx context.Context, s *Subject) error {
	args := m.Called(ctx, s)
	return args.Error(0)
}

func (m *mockSubjectService) DeleteSubject(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func setup() (*mockSubjectService, *SubjectHandler) {
	svc := new(mockSubjectService)
	return svc, NewSubjectHandler(svc)
}

func TestCreateSubject(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		setupMock  func(*mockSubjectService)
		wantStatus int
		wantName   string
	}{
		{
			name: "happy path",
			body: `{"name":"Alice","subject_type":"HUMAN"}`,
			setupMock: func(s *mockSubjectService) {
				s.On("CreateSubject", mock.Anything, mock.MatchedBy(func(sub *Subject) bool {
					return sub.Name == "Alice"
				})).Return(nil)
			},
			wantStatus: http.StatusCreated,
			wantName:   "Alice",
		},
		{
			name:       "invalid json",
			body:       `{bad}`,
			setupMock:  func(s *mockSubjectService) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "service error",
			body: `{"name":"Bob"}`,
			setupMock: func(s *mockSubjectService) {
				s.On("CreateSubject", mock.Anything, mock.Anything).Return(errors.New("db error"))
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, h := setup()
			tc.setupMock(svc)

			r := mux.NewRouter()
			r.HandleFunc("/subjects", h.CreateSubject).Methods(http.MethodPost)

			req := httptest.NewRequest(http.MethodPost, "/subjects", bytes.NewBufferString(tc.body))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code)
			if tc.wantName != "" {
				var got Subject
				assert.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
				assert.Equal(t, tc.wantName, got.Name)
			}
			svc.AssertExpectations(t)
		})
	}
}

func TestGetAllSubjects(t *testing.T) {
	tests := []struct {
		name       string
		setupMock  func(*mockSubjectService)
		wantStatus int
		wantLen    int
	}{
		{
			name: "happy path",
			setupMock: func(s *mockSubjectService) {
				s.On("GetAllSubjects", mock.Anything).Return([]Subject{
					{ID: uuid.New(), Name: "Alice"},
					{ID: uuid.New(), Name: "Bob"},
				}, nil)
			},
			wantStatus: http.StatusOK,
			wantLen:    2,
		},
		{
			name: "service error",
			setupMock: func(s *mockSubjectService) {
				s.On("GetAllSubjects", mock.Anything).Return([]Subject{}, errors.New("db error"))
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, h := setup()
			tc.setupMock(svc)

			r := mux.NewRouter()
			r.HandleFunc("/subjects", h.GetAllSubjects).Methods(http.MethodGet)

			req := httptest.NewRequest(http.MethodGet, "/subjects", nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code)
			if tc.wantLen > 0 {
				var got []Subject
				assert.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
				assert.Len(t, got, tc.wantLen)
			}
			svc.AssertExpectations(t)
		})
	}
}

func TestGetSubjectByID(t *testing.T) {
	subjID := uuid.New()

	tests := []struct {
		name       string
		id         string
		setupMock  func(*mockSubjectService)
		wantStatus int
	}{
		{
			name: "happy path",
			id:   subjID.String(),
			setupMock: func(s *mockSubjectService) {
				s.On("GetSubjectByID", mock.Anything, subjID.String()).Return(&Subject{ID: subjID, Name: "Alice"}, nil)
			},
			wantStatus: http.StatusOK,
		},
		{
			// A row that is not there reaches the handler as pgx.ErrNoRows: every
			// repository returns it from QueryRow(...).Scan and every service passes
			// it through. This case used to mock (nil, nil) — a value no real service
			// can produce — so it went green against a handler that answered 500 to
			// every missing row.
			name: "not found",
			id:   subjID.String(),
			setupMock: func(s *mockSubjectService) {
				s.On("GetSubjectByID", mock.Anything, subjID.String()).Return(nil, pgx.ErrNoRows)
			},
			wantStatus: http.StatusNotFound,
		},
		{
			// The nil-without-error branch is still in the handler and still answers
			// 404. Nothing produces it today; this keeps it covered rather than
			// leaving an untested branch behind.
			name: "service returned nothing",
			id:   subjID.String(),
			setupMock: func(s *mockSubjectService) {
				s.On("GetSubjectByID", mock.Anything, subjID.String()).Return(nil, nil)
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "service error",
			id:   subjID.String(),
			setupMock: func(s *mockSubjectService) {
				s.On("GetSubjectByID", mock.Anything, subjID.String()).Return(nil, errors.New("db error"))
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			// A typo'd id must answer 400 before it reaches the service — no
			// expectation is set, so any service call fails the test.
			name:       "invalid uuid",
			id:         "not-a-uuid",
			setupMock:  func(s *mockSubjectService) {},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, h := setup()
			tc.setupMock(svc)

			r := mux.NewRouter()
			r.HandleFunc("/subjects/{id}", h.GetSubjectByID).Methods(http.MethodGet)

			req := httptest.NewRequest(http.MethodGet, "/subjects/"+tc.id, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code)
			svc.AssertExpectations(t)
		})
	}
}

func TestUpdateSubject(t *testing.T) {
	subjID := uuid.New()

	tests := []struct {
		name       string
		id         string
		body       string
		setupMock  func(*mockSubjectService)
		wantStatus int
	}{
		{
			name: "happy path",
			id:   subjID.String(),
			body: `{"name":"Updated","subject_type":"AI"}`,
			setupMock: func(s *mockSubjectService) {
				s.On("UpdateSubject", mock.Anything, mock.MatchedBy(func(sub *Subject) bool {
					return sub.ID == subjID && sub.Name == "Updated"
				})).Return(nil)
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "invalid json",
			id:         subjID.String(),
			body:       `{bad}`,
			setupMock:  func(s *mockSubjectService) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid uuid",
			id:         "not-a-uuid",
			body:       `{"name":"Updated"}`,
			setupMock:  func(s *mockSubjectService) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "service error",
			id:   subjID.String(),
			body: `{"name":"Updated"}`,
			setupMock: func(s *mockSubjectService) {
				s.On("UpdateSubject", mock.Anything, mock.Anything).Return(errors.New("db error"))
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, h := setup()
			tc.setupMock(svc)

			r := mux.NewRouter()
			r.HandleFunc("/subjects/{id}", h.UpdateSubject).Methods(http.MethodPut)

			req := httptest.NewRequest(http.MethodPut, "/subjects/"+tc.id, bytes.NewBufferString(tc.body))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code)
			svc.AssertExpectations(t)
		})
	}
}

func TestDeleteSubject(t *testing.T) {
	subjID := uuid.New()

	tests := []struct {
		name       string
		id         string
		setupMock  func(*mockSubjectService)
		wantStatus int
	}{
		{
			name: "happy path",
			id:   subjID.String(),
			setupMock: func(s *mockSubjectService) {
				s.On("DeleteSubject", mock.Anything, subjID.String()).Return(nil)
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "service error",
			id:   subjID.String(),
			setupMock: func(s *mockSubjectService) {
				s.On("DeleteSubject", mock.Anything, subjID.String()).Return(errors.New("db error"))
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			// A typo'd id must answer 400 before it reaches the service — no
			// expectation is set, so any service call fails the test.
			name:       "invalid uuid",
			id:         "not-a-uuid",
			setupMock:  func(s *mockSubjectService) {},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, h := setup()
			tc.setupMock(svc)

			r := mux.NewRouter()
			r.HandleFunc("/subjects/{id}", h.DeleteSubject).Methods(http.MethodDelete)

			req := httptest.NewRequest(http.MethodDelete, "/subjects/"+tc.id, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code)
			svc.AssertExpectations(t)
		})
	}
}
