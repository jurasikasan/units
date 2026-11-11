package subject

import (
	"encoding/json"

	"github.com/google/uuid"
)

type Subject struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	// Nick is how people and models address a subject: unique across every
	// subject ever created, deleted ones included, so a nick never comes to
	// mean somebody else; set once at creation and never changed. The
	// database holds both rules (migrations/0001); the service only says why.
	Nick        string `json:"nick"`
	Bio         string `json:"bio,omitempty"`
	SubjectType string `json:"subject_type"`
	// Preferences is a JSON object this service stores and returns as it came.
	// Its keys belong to whoever reads them; the registry never looks inside.
	Preferences json.RawMessage `json:"preferences,omitempty" swaggertype:"object"`
}
