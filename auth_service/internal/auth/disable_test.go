package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/jurasikasan/units/auth_service/internal/account"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeDisabler struct {
	ops *[]string
	id  uuid.UUID
	err error
}

func (f *fakeDisabler) DisableByUsername(ctx context.Context, username string) (uuid.UUID, error) {
	*f.ops = append(*f.ops, "DisableByUsername "+username)
	return f.id, f.err
}

type fakeKiller struct {
	ops    *[]string
	err    error
	called bool
}

func (f *fakeKiller) DeleteAllForPrincipal(ctx context.Context, principalID uuid.UUID) error {
	*f.ops = append(*f.ops, "DeleteAllForPrincipal "+principalID.String())
	f.called = true
	return f.err
}

func TestDisable_SetsFlagThenKillsSessions(t *testing.T) {
	id := uuid.New()
	var ops []string
	disabler := &fakeDisabler{ops: &ops, id: id}
	killer := &fakeKiller{ops: &ops}

	disabled, err := Disable(context.Background(), disabler, killer, "alice")
	require.NoError(t, err)
	assert.Equal(t, id, disabled)

	require.Len(t, ops, 2)
	assert.Equal(t, "DisableByUsername alice", ops[0],
		"the disabled flag must be set first, or a login in the gap mints a session that survives")
	assert.Equal(t, "DeleteAllForPrincipal "+id.String(), ops[1],
		"sessions are killed for the id the disable returned")
}

func TestDisable_UnknownUsername_NoSessionsTouched(t *testing.T) {
	var ops []string
	disabler := &fakeDisabler{ops: &ops, err: account.ErrAccountNotFound}
	killer := &fakeKiller{ops: &ops}

	_, err := Disable(context.Background(), disabler, killer, "ghost")
	require.Error(t, err)
	assert.True(t, errors.Is(err, account.ErrAccountNotFound),
		"not-found must survive so the operator can tell it from a broken database")
	assert.False(t, killer.called, "no session may be touched when there is no account")
}

func TestDisable_SessionKillFailure_IsReturned(t *testing.T) {
	id := uuid.New()
	killErr := errors.New("redis is down")
	var ops []string
	disabler := &fakeDisabler{ops: &ops, id: id}
	killer := &fakeKiller{ops: &ops, err: killErr}

	_, err := Disable(context.Background(), disabler, killer, "alice")
	require.Error(t, err)
	assert.True(t, errors.Is(err, killErr), "a failed session kill must be surfaced, not swallowed")
	require.NotEmpty(t, ops)
	assert.Equal(t, "DisableByUsername alice", ops[0], "the flag was set before the kill was attempted")
}
