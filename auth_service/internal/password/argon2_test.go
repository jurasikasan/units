package password

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashAndVerify(t *testing.T) {
	hash, err := Hash("correct horse battery staple")
	require.NoError(t, err)

	ok, err := Verify("correct horse battery staple", hash)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = Verify("Correct horse battery staple", hash)
	require.NoError(t, err)
	assert.False(t, ok, "verification must be case sensitive")

	ok, err = Verify("", hash)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestHashIsSalted(t *testing.T) {
	first, err := Hash("same password")
	require.NoError(t, err)
	second, err := Hash("same password")
	require.NoError(t, err)

	assert.NotEqual(t, first, second)

	for _, h := range []string{first, second} {
		ok, err := Verify("same password", h)
		require.NoError(t, err)
		assert.True(t, ok)
	}
}

func TestHashFormat(t *testing.T) {
	hash, err := Hash("whatever")
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(hash, "$argon2id$v=19$"), "got %q", hash)
	assert.Len(t, strings.Split(hash, "$"), 6)
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	cases := map[string]string{
		"empty":             "",
		"not phc":           "plaintext-password",
		"wrong algorithm":   "$argon2i$v=19$m=65536,t=1,p=4$c2FsdA$aGFzaA",
		"missing sections":  "$argon2id$v=19$m=65536,t=1,p=4",
		"bad base64 salt":   "$argon2id$v=19$m=65536,t=1,p=4$!!!!$aGFzaA",
		"unsupported ver":   "$argon2id$v=99$m=65536,t=1,p=4$c2FsdA$aGFzaA",
		"unparsable params": "$argon2id$v=19$m=abc,t=1,p=4$c2FsdA$aGFzaA",
	}

	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			ok, err := Verify("anything", encoded)

			assert.Error(t, err)
			assert.False(t, ok)
		})
	}
}

func TestDummyVerifyDoesNotPanic(t *testing.T) {
	assert.NotPanics(t, func() { DummyVerify("some password") })
}
