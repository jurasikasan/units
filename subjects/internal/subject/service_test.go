package subject

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type mockSubjectRepository struct {
	mock.Mock
}

func (m *mockSubjectRepository) Create(ctx context.Context, s *Subject) error {
	args := m.Called(ctx, s)
	return args.Error(0)
}

func (m *mockSubjectRepository) GetAll(ctx context.Context) ([]Subject, error) {
	args := m.Called(ctx)
	return args.Get(0).([]Subject), args.Error(1)
}

func (m *mockSubjectRepository) GetByID(ctx context.Context, id string) (*Subject, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*Subject), args.Error(1)
}

func (m *mockSubjectRepository) Update(ctx context.Context, s *Subject) error {
	args := m.Called(ctx, s)
	return args.Error(0)
}

func (m *mockSubjectRepository) Delete(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func TestDefaultSubjectService_CreateSubject(t *testing.T) {
	tests := []struct {
		name      string
		input     *Subject
		repoErr   error
		wantErr   bool
		checkUUID bool
		clientID  uuid.UUID
	}{
		{
			name:      "generates uuid",
			input:     &Subject{Name: "Alice", Nick: "alice", SubjectType: "HUMAN"},
			checkUUID: true,
		},
		{
			name:      "ignoring incoming id",
			input:     &Subject{Name: "Carol", Nick: "carol", SubjectType: "human"},
			checkUUID: true,
			clientID:  uuid.New(),
		},
		{
			name:    "repo error propagated",
			input:   &Subject{Name: "Bob", Nick: "bob", SubjectType: "ai"},
			repoErr: errors.New("db error"),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := new(mockSubjectRepository)
			svc := NewDefaultSubjectService(repo)

			repo.On("Create", mock.Anything, mock.MatchedBy(func(s *Subject) bool {
				return s.ID != uuid.Nil
			})).Return(tc.repoErr)

			// A real, non-nil id: the service must replace it, not just
			// fill a zero one in.
			if tc.clientID != uuid.Nil {
				tc.input.ID = tc.clientID
			}

			err := svc.CreateSubject(context.Background(), tc.input)

			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			if tc.checkUUID {
				assert.NotEqual(t, uuid.Nil, tc.input.ID)
			}
			if tc.clientID != uuid.Nil {
				assert.NotEqual(t, tc.clientID, tc.input.ID, "the stored subject keeps the client-sent ID; the server must issue its own")
			}
			repo.AssertExpectations(t)
		})
	}
}
