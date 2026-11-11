package subject

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
)

// The nick's rules. Lower-case Latin, digits, "-"
// and "_", starting with a letter: a nick is typed in mentions and lands in
// URLs, where case and Cyrillic make look-alikes. The same pattern is the
// CHECK in schema.sql — the database is what holds it; this only lets the
// refusal say which rule was broken instead of a constraint name.
var nickPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

var (
	// ErrInvalidNick: the nick breaks the alphabet or the length.
	ErrInvalidNick = errors.New("nick must be 2-32 characters of a-z, 0-9, - and _, starting with a letter")
	// ErrNickTaken: another subject, live or deleted, has this nick.
	ErrNickTaken = errors.New("nick is taken")
	// ErrNickImmutable: an update tried to change the nick.
	ErrNickImmutable = errors.New("nick cannot be changed once set")
	// ErrEmptyType: subject_type is TEXT NOT NULL and its values are the
	// deployment's (human, ai, …); an empty one is refused, not stored.
	ErrEmptyType = errors.New("subject_type is required")
	// ErrPreferencesNotObject: preferences, when present, is a JSON object.
	ErrPreferencesNotObject = errors.New("preferences must be a JSON object")
)

// SubjectService defines the business logic interface for subjects.
type SubjectService interface {
	CreateSubject(ctx context.Context, subject *Subject) error
	GetAllSubjects(ctx context.Context) ([]Subject, error)
	GetSubjectByID(ctx context.Context, id string) (*Subject, error)
	UpdateSubject(ctx context.Context, subject *Subject) error
	DeleteSubject(ctx context.Context, id string) error
}

// DefaultSubjectService is the default implementation of SubjectService.
type DefaultSubjectService struct {
	repo SubjectRepository
}

// NewDefaultSubjectService creates a new DefaultSubjectService.
func NewDefaultSubjectService(repo SubjectRepository) *DefaultSubjectService {
	return &DefaultSubjectService{repo: repo}
}

// CreateSubject handles the logic for creating a new subject.
func (s *DefaultSubjectService) CreateSubject(ctx context.Context, subject *Subject) error {
	if subject.SubjectType == "" {
		return ErrEmptyType
	}
	if !prefsIsObject(subject.Preferences) {
		return ErrPreferencesNotObject
	}
	if !nickPattern.MatchString(subject.Nick) {
		return fmt.Errorf("%w: %q", ErrInvalidNick, subject.Nick)
	}
	subject.ID = uuid.New()
	return s.repo.Create(ctx, subject)
}

// GetAllSubjects retrieves all subjects.
func (s *DefaultSubjectService) GetAllSubjects(ctx context.Context) ([]Subject, error) {
	return s.repo.GetAll(ctx)
}

// GetSubjectByID retrieves a subject by its ID.
func (s *DefaultSubjectService) GetSubjectByID(ctx context.Context, id string) (*Subject, error) {
	return s.repo.GetByID(ctx, id)
}

// UpdateSubject updates an existing subject.
// The nick is not written by an update. One the request leaves out is kept;
// one it carries must be the stored one, so a client that edits it learns that
// it did not happen instead of getting a 200 for a change that was dropped.
func (s *DefaultSubjectService) UpdateSubject(ctx context.Context, subject *Subject) error {
	if subject.SubjectType == "" {
		return ErrEmptyType
	}
	if !prefsIsObject(subject.Preferences) {
		return ErrPreferencesNotObject
	}
	stored, err := s.repo.GetByID(ctx, subject.ID.String())
	if err != nil {
		return err
	}
	if subject.Nick != "" && subject.Nick != stored.Nick {
		return fmt.Errorf("%w: it is %q", ErrNickImmutable, stored.Nick)
	}
	subject.Nick = stored.Nick
	return s.repo.Update(ctx, subject)
}

// DeleteSubject soft-deletes a subject. Which subjects a deployment must keep
// is its own policy, enforced at its own door, not here.
func (s *DefaultSubjectService) DeleteSubject(ctx context.Context, id string) error {
	if _, err := s.repo.GetByID(ctx, id); err != nil {
		return err
	}
	return s.repo.Delete(ctx, id)
}

// prefsIsObject: absent, null or a JSON object. The decoder has already
// checked it is valid JSON; only its kind is left to look at.
func prefsIsObject(p json.RawMessage) bool {
	t := bytes.TrimSpace(p)
	return len(t) == 0 || string(t) == "null" || t[0] == '{'
}
