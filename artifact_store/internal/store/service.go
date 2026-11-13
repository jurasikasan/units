package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"
)

// DefaultMaxSize is the 512 MiB limit of CONTRACT.md section 5. It is a
// variable so a test can set it small; a body over it is refused with 413
// before anything is stored.
const DefaultMaxSize = 512 << 20

// The service's error vocabulary; the handler maps each of them to its
// status in exactly one place (see handler.go).
var (
	ErrBadIdent    = errors.New("space or name breaks its alphabet")
	ErrBadBody     = errors.New("cannot read the request body")
	ErrNotFound    = errors.New("no such file")
	ErrTooLarge    = errors.New("body exceeds the size limit")
	ErrUnavailable = errors.New("MinIO or Postgres unavailable")
)

// ObjectStore is where the bytes live. MinioStore is the real
// implementation; unit tests stand a fake in its place.
type ObjectStore interface {
	// Put stores data under key and records meta with it, so a later
	// List can answer the metadata without downloading the bytes.
	// It is all or nothing: on failure the previous file under key
	// (or no file) stays.
	Put(ctx context.Context, key string, data []byte, meta ObjectMeta) error
	// Get returns the bytes and the stored Content-Type. A missing
	// object is ErrNotFound; anything else is ErrUnavailable.
	Get(ctx context.Context, key string) (data []byte, contentType string, err error)
	// List returns the objects whose key starts with prefix, in key
	// order; an empty result is an empty, non-nil slice. A failure of
	// the store underneath is ErrUnavailable.
	List(ctx context.Context, prefix string) ([]Object, error)
	// Delete removes the object under key. A key that is not there is
	// ErrNotFound; anything else is ErrUnavailable.
	Delete(ctx context.Context, key string) error
}

// Service is what the handlers talk to.
type Service interface {
	Put(ctx context.Context, space, name, contentType, createdBy string, r io.Reader) (*File, error)
	Get(ctx context.Context, space, name string) (data []byte, contentType string, err error)
	List(ctx context.Context, space, prefix string) ([]File, error)
	Delete(ctx context.Context, space, name string) error
}

// DefaultService is the Service on top of an ObjectStore.
type DefaultService struct {
	Store   ObjectStore
	MaxSize int64
}

// NewService builds the service on the real store: the names in the
// store's own Postgres (DATABASE_URL, schema migrated on start), the
// bytes in MinIO under their sha256 (MINIO_*, bucket MINIO_BUCKET,
// default "artifact-store", created on first upload by the shared
// uploader). A dependency that is down is a 503 on the routes, not a
// failed start. retention is the per-space rule read from RETENTION at
// start (CONTRACT.md section 5); empty means nothing expires.
// sweepPeriod is the STORE_SWEEP_PERIOD read at start (CONTRACT.md
// section 2): how often StartSweep runs the expiry and the sweep,
// after the pass it runs right after start.
func NewService(ctx context.Context, retention Retention, sweepPeriod time.Duration) *DefaultService {
	store := NewMinioStore(ctx)
	store.Retention = retention
	store.SweepPeriod = sweepPeriod
	return &DefaultService{Store: store, MaxSize: DefaultMaxSize}
}

// StartSweep runs the expiry and the removal of the bytes no name
// points to any more once right after start, then every SweepPeriod
// (CONTRACT.md section 5). A store that has no sweep does nothing, so
// a fake stood in its place needs no extra method.
func (s *DefaultService) StartSweep(ctx context.Context) {
	if sw, ok := s.Store.(interface{ StartSweep(context.Context) }); ok {
		sw.StartSweep(ctx)
	}
}

// Put stores the body under space/name and returns the file's metadata.
// The sha256 is computed while the body is read, and the size limit is
// checked before anything is stored.
func (s *DefaultService) Put(ctx context.Context, space, name, contentType, createdBy string, r io.Reader) (*File, error) {
	if !SpaceValid(space) || !NameValid(name) {
		return nil, ErrBadIdent
	}

	hash := sha256.New()
	var buf bytes.Buffer
	if _, err := io.Copy(io.MultiWriter(&buf, hash), io.LimitReader(r, s.MaxSize+1)); err != nil {
		return nil, ErrBadBody
	}
	if int64(buf.Len()) > s.MaxSize {
		return nil, ErrTooLarge
	}

	now := time.Now().UTC()
	meta := ObjectMeta{
		Sha256:      hex.EncodeToString(hash.Sum(nil)),
		ContentType: contentType,
		CreatedBy:   createdBy,
		UpdatedAt:   now,
	}

	key := ObjectKey(space, name)
	if err := s.Store.Put(ctx, key, buf.Bytes(), meta); err != nil {
		return nil, err
	}

	file := &File{
		Name:        name,
		Sha256:      meta.Sha256,
		Size:        int64(buf.Len()),
		ContentType: meta.ContentType,
		UpdatedAt:   now.Format(time.RFC3339),
	}
	if createdBy != "" {
		v := createdBy
		file.CreatedBy = &v
	}
	return file, nil
}

// Get returns the bytes and the stored Content-Type of space/name.
func (s *DefaultService) Get(ctx context.Context, space, name string) ([]byte, string, error) {
	if !SpaceValid(space) || !NameValid(name) {
		return nil, "", ErrBadIdent
	}
	return s.Store.Get(ctx, ObjectKey(space, name))
}

// List returns the space's files whose name starts with prefix (all of
// them without it), as Files, ordered by name (CONTRACT.md section 3).
// A space with no matching file answers an empty, non-nil slice; a space
// that breaks the alphabet is refused before the store is reached.
func (s *DefaultService) List(ctx context.Context, space, prefix string) ([]File, error) {
	if !SpaceValid(space) {
		return nil, ErrBadIdent
	}

	objects, err := s.Store.List(ctx, space+"/"+prefix)
	if err != nil {
		return nil, err
	}

	files := make([]File, 0, len(objects))
	for _, o := range objects {
		file := File{
			Name:        strings.TrimPrefix(o.Key, space+"/"),
			Sha256:      o.Meta.Sha256,
			Size:        o.Size,
			ContentType: o.Meta.ContentType,
			UpdatedAt:   o.Meta.UpdatedAt.UTC().Format(time.RFC3339),
		}
		if o.Meta.CreatedBy != "" {
			v := o.Meta.CreatedBy
			file.CreatedBy = &v
		}
		files = append(files, file)
	}
	return files, nil
}

// Delete removes the file under space/name. A file that is not there is
// ErrNotFound (CONTRACT.md section 3: DELETE answers 204, 404); a space
// or name that breaks the alphabet is refused before the store is reached.
func (s *DefaultService) Delete(ctx context.Context, space, name string) error {
	if !SpaceValid(space) || !NameValid(name) {
		return ErrBadIdent
	}
	return s.Store.Delete(ctx, ObjectKey(space, name))
}
