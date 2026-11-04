package account

import (
	"context"
	"errors"
	"testing"

	"github.com/jurasikasan/units/auth_service/internal/password"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRepo struct {
	row *Account
	err error
}

func (f *fakeRepo) FindByUsername(ctx context.Context, username string) (*Account, error) {
	return f.row, f.err
}

func (f *fakeRepo) FindByID(ctx context.Context, id uuid.UUID) (*Account, error) {
	return f.row, f.err
}

func TestAuthenticate_UnknownUserCallsDummyVerifyOnce(t *testing.T) {
	dummyCalls := 0
	svc := &Service{
		repo:        &fakeRepo{},
		dummyVerify: func(string) { dummyCalls++ },
	}

	a, err := svc.Authenticate(context.Background(), "ghost", "whatever")
	require.NoError(t, err)
	assert.Nil(t, a)
	assert.Equal(t, 1, dummyCalls, "unknown user must cost exactly one dummy verify, so timing cannot enumerate usernames")
}

func TestAuthenticate_MalformedHashCallsDummyVerifyOnce(t *testing.T) {
	row := &Account{ID: uuid.New(), Username: "broken", passwordHash: "not a hash at all"}
	dummyCalls := 0
	svc := &Service{
		repo:        &fakeRepo{row: row},
		dummyVerify: func(string) { dummyCalls++ },
	}

	a, err := svc.Authenticate(context.Background(), "broken", "whatever")
	require.Error(t, err, "a corrupt hash is still surfaced as an error, wrapped")
	assert.True(t, errors.Is(err, password.ErrMalformedHash))
	assert.Nil(t, a)
	assert.Equal(t, 1, dummyCalls, "a hash the decoder rejects must cost exactly one dummy verify")
}
