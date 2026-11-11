package subject

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Preferences belong to whoever reads them: keys this service has never heard
// of, nested values and the difference between null and [] all come back as
// they went in. A typed struct here used to drop every key it did not name.
func TestPrefsRoundTripKeepsUnknownKeys(t *testing.T) {
	in := json.RawMessage(`{"voice":"silero_baya","x-own":{"deep":[1,null,"a"]},"tools":null,"none":[]}`)
	v := prefsValue(in)
	require.NotNil(t, v)
	out := scanPrefs([]byte(v.(string)))
	assert.JSONEq(t, string(in), string(out))
}

func TestPrefsAbsentOrNullStoreSQLNull(t *testing.T) {
	assert.Nil(t, prefsValue(nil))
	assert.Nil(t, prefsValue(json.RawMessage("null")))
	assert.Nil(t, scanPrefs(nil))
}

// A subject's preferences are an object or nothing; an array or a scalar is a
// malformed request, refused before the repository is touched.
func TestCreateSubject_PreferencesMustBeObject(t *testing.T) {
	for _, p := range []string{`[]`, `"x"`, `42`, `true`} {
		repo := new(mockSubjectRepository)
		err := NewDefaultSubjectService(repo).CreateSubject(context.Background(),
			&Subject{Nick: "alice", SubjectType: "ai", Preferences: json.RawMessage(p)})
		assert.ErrorIs(t, err, ErrPreferencesNotObject, p)
		repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	}
	for _, p := range []string{``, `null`, ` {"a":1}`} {
		repo := new(mockSubjectRepository)
		repo.On("Create", mock.Anything, mock.Anything).Return(nil)
		err := NewDefaultSubjectService(repo).CreateSubject(context.Background(),
			&Subject{Nick: "alice", SubjectType: "ai", Preferences: json.RawMessage(p)})
		assert.NoError(t, err, p)
	}
}
