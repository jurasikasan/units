package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // the "pgx" driver for database/sql
	minio "github.com/minio/minio-go/v7"

	"github.com/jurasikasan/units/artifact_store/internal/objects"
)

// Sector 5 of CONTRACT.md: identical bytes are stored once. The bytes live
// in MinIO under their sha256, and the names — which name points to which
// bytes — live in Postgres. Replacing and deleting a file move the name
// only; a background sweep removes the bytes no name points to any more.

// DefaultSweepPeriod is how often the sweep runs. It is a variable so a
// test can set it; a store built by NewMinioStore copies it into its
// SweepPeriod.
var DefaultSweepPeriod = time.Hour

// NamedFile is one row of the name table: a name in a space pointing to
// the bytes with Sha256. CreatedBy is "" when the PUT had no
// X-Principal-Id; Postgres stores that as NULL and hands it back as "".
type NamedFile struct {
	Space       string
	Name        string
	Sha256      string
	Size        int64
	ContentType string
	CreatedBy   string
	UpdatedAt   time.Time
}

// StoredBytes is one object of the byte store, as the sweep sees it: the
// bytes are keyed by their sha256, and Modified is when the object was
// last written.
type StoredBytes struct {
	Key      string
	Size     int64
	Modified time.Time
}

// ByteStore is where the bytes live, keyed by their sha256: writing the
// same bytes twice is one object.
type ByteStore interface {
	// Put stores data under its sha256, replacing whatever is there.
	Put(ctx context.Context, sha string, data []byte, contentType string) error
	// Get returns the bytes under sha. A missing sha is ErrNotFound;
	// anything else is ErrUnavailable.
	Get(ctx context.Context, sha string) ([]byte, error)
	// Delete removes the object under sha. The sweep is the only caller:
	// a key that is not there is not an error worth aborting over.
	Delete(ctx context.Context, sha string) error
	// List returns every object, in key order, with its modified time.
	List(ctx context.Context) ([]StoredBytes, error)
}

// NameDB is the name table in Postgres: which name points to which bytes.
type NameDB interface {
	// Upsert stores the row, replacing a previous row under the same
	// name. It is the all-or-nothing step of a PUT: on failure the
	// previous name (or no name) stays as it was.
	Upsert(ctx context.Context, file NamedFile) error
	// Get returns the row under space/name; ok is false when there is no
	// such name.
	Get(ctx context.Context, space, name string) (NamedFile, bool, error)
	// List returns the rows of space whose name starts with namePrefix
	// (all of them when it is empty), in name order.
	List(ctx context.Context, space, namePrefix string) ([]NamedFile, error)
	// Delete removes the row and reports whether it was there. It moves
	// the name only: the bytes stay, and the sweep takes them if no name
	// points to them any more.
	Delete(ctx context.Context, space, name string) (bool, error)
	// Expire removes the rows of space whose last write is before
	// olderThan, and nothing else: it is the "a file not written for
	// that long is removed" step of a retention rule (CONTRACT.md
	// section 5). The caller passes only the spaces with a rule; a space
	// without one is not touched.
	Expire(ctx context.Context, space string, olderThan time.Time) error
	// Referenced returns the set of sha256 some name points to.
	Referenced(ctx context.Context) (map[string]bool, error)
}

// MinioStore is the ObjectStore of the real deployment: names in
// Postgres (NameDB), bytes in MinIO under their sha256 (ByteStore).
// The unit tests stand a fake in for each of the two.
type MinioStore struct {
	names NameDB
	bytes ByteStore

	// Retention is the per-space rule read from RETENTION at start
	// (CONTRACT.md section 5): a file not written for its rule's time is
	// removed; a space without a rule — "never", or not listed — never
	// expires. Empty means nothing expires.
	Retention Retention

	// SweepPeriod is how often StartSweep runs the expiry and the sweep.
	// It is a variable so a test can set it; NewMinioStore takes the
	// default.
	SweepPeriod time.Duration
}

// NewMinioStore builds the store from the environment: MINIO_* (the
// shared client), MINIO_BUCKET (default "artifact-store", created on
// first upload) and DATABASE_URL (its own Postgres, schema migrated on
// start — best effort, and retried on every operation if Postgres is
// not there yet).
func NewMinioStore(ctx context.Context) *MinioStore {
	names := NewPostgresNames(ctx)
	go func() {
		if err := names.EnsureSchema(ctx); err != nil {
			// Postgres is not there yet: every operation retries the
			// migration, so this is a logged heads-up, not a failure.
			log.Printf("artifact_store: Postgres schema not ready yet: %v", err)
		}
	}()
	return &MinioStore{
		names:       names,
		bytes:       NewMinioBytes(ctx),
		SweepPeriod: DefaultSweepPeriod,
	}
}

// splitKey cuts a key the service passed (space + "/" + name) back into
// its two halves. A key without a separator cannot name anything; the
// name table simply knows no such name.
func splitKey(key string) (space, name string) {
	i := strings.IndexByte(key, '/')
	if i <= 0 {
		return "", key
	}
	return key[:i], key[i+1:]
}

// Put stores the bytes under their sha256 and then points the name at
// them. The bytes go first: a name may only ever point at bytes that are
// already there, and writing the same bytes again is one object, so
// equal bytes under two names are one object. If the name table fails,
// the previous name (or no name) stays as it was — PUT is all or
// nothing — and a possibly orphaned object is the sweep's to take.
func (m *MinioStore) Put(ctx context.Context, key string, data []byte, meta ObjectMeta) error {
	space, name := splitKey(key)
	if err := m.bytes.Put(ctx, meta.Sha256, data, meta.ContentType); err != nil {
		return err
	}
	return m.names.Upsert(ctx, NamedFile{
		Space:       space,
		Name:        name,
		Sha256:      meta.Sha256,
		Size:        int64(len(data)),
		ContentType: meta.ContentType,
		CreatedBy:   meta.CreatedBy,
		UpdatedAt:   meta.UpdatedAt,
	})
}

// Get follows the name to its bytes and answers with them and the
// Content-Type the name table recorded. A name that is not there is
// ErrNotFound.
func (m *MinioStore) Get(ctx context.Context, key string) ([]byte, string, error) {
	space, name := splitKey(key)
	file, ok, err := m.names.Get(ctx, space, name)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", ErrNotFound
	}
	data, err := m.bytes.Get(ctx, file.Sha256)
	if err != nil {
		return nil, "", err
	}
	return data, file.ContentType, nil
}

// List answers the space's names from the name table, in name order, as
// the Object facts the service turns into Files.
func (m *MinioStore) List(ctx context.Context, prefix string) ([]Object, error) {
	space, namePrefix := splitKey(prefix)
	files, err := m.names.List(ctx, space, namePrefix)
	if err != nil {
		return nil, err
	}
	objects := make([]Object, 0, len(files))
	for _, file := range files {
		objects = append(objects, Object{
			Key:  ObjectKey(file.Space, file.Name),
			Size: file.Size,
			Meta: ObjectMeta{
				Sha256:      file.Sha256,
				ContentType: file.ContentType,
				CreatedBy:   file.CreatedBy,
				UpdatedAt:   file.UpdatedAt,
			},
		})
	}
	return objects, nil
}

// Delete removes the name. A name that is not there is ErrNotFound. The
// bytes are not touched: another name may still point at them, and when
// none does, the sweep takes them.
func (m *MinioStore) Delete(ctx context.Context, key string) error {
	space, name := splitKey(key)
	existed, err := m.names.Delete(ctx, space, name)
	if err != nil {
		return err
	}
	if !existed {
		return ErrNotFound
	}
	return nil
}

// Expire removes, for each space with a rule, the files whose last write
// is older than the rule (CONTRACT.md section 5: "a file not written for
// that long is removed"). A space without a rule — set to "never" or not
// listed at all — is not touched. It moves the names only: the bytes a
// freed name pointed to are the sweep's to take, and the sweep never
// touches the bytes a name still points to.
func (m *MinioStore) Expire(ctx context.Context) error {
	now := time.Now()

	var firstErr error
	for space, keepFor := range m.Retention {
		if err := m.names.Expire(ctx, space, now.Add(-keepFor)); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Sweep removes the bytes no name points to any more (CONTRACT.md
// section 5). It never touches the bytes a name still points to, and it
// never touches the bytes a PUT is writing at that moment: a PUT lays
// down its bytes before its name, so those bytes are newer than the
// moment the sweep began.
func (m *MinioStore) Sweep(ctx context.Context) error {
	start := time.Now()

	referenced, err := m.names.Referenced(ctx)
	if err != nil {
		return err
	}
	objects, err := m.bytes.List(ctx)
	if err != nil {
		return err
	}

	var firstErr error
	for _, obj := range objects {
		if referenced[obj.Key] {
			continue
		}
		if !obj.Modified.Before(start) {
			// Written at or after the sweep began: a PUT may be in
			// flight. Take it next time; it will be gone or referenced.
			continue
		}
		if firstErr == nil {
			if err := m.bytes.Delete(ctx, obj.Key); err != nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// StartSweep runs the expiry and the sweep once right after start, then
// every SweepPeriod until ctx is done (CONTRACT.md section 5: "expiry
// and the removal of unnamed bytes run once right after start, then
// every STORE_SWEEP_PERIOD. A process that restarts often still cleans
// up"). The expiry moves the names of the files past their rule's time,
// and the sweep takes the bytes no name points to any more. A period
// not set gets the default. A dependency that is down is logged, not a
// failure: the next pass retries.
func (m *MinioStore) StartSweep(ctx context.Context) {
	if m.SweepPeriod <= 0 {
		m.SweepPeriod = DefaultSweepPeriod
	}
	ticker := time.NewTicker(m.SweepPeriod)
	go func() {
		defer ticker.Stop()
		pass := func() {
			if err := m.Expire(ctx); err != nil {
				log.Printf("artifact_store expiry: %v", err)
			}
			if err := m.Sweep(ctx); err != nil {
				log.Printf("artifact_store sweep: %v", err)
			}
		}
		pass()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pass()
			}
		}
	}()
}

// MinioBytes is the ByteStore on MinIO: the objects
// are the bytes under their sha256, the only key the bucket carries.
type MinioBytes struct {
	client *minio.Client
	bucket string
}

// NewMinioBytes builds the byte store from the environment.
func NewMinioBytes(ctx context.Context) *MinioBytes {
	bucket := os.Getenv("MINIO_BUCKET")
	if bucket == "" {
		bucket = "artifact-store"
	}
	return &MinioBytes{client: objects.NewClient(ctx), bucket: bucket}
}

// Put writes the bytes under their sha256; the bucket is created on
// demand.
func (b *MinioBytes) Put(ctx context.Context, sha string, data []byte, contentType string) error {
	if err := objects.EnsureBucket(ctx, b.client, b.bucket); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if _, err := b.client.PutObject(ctx, b.bucket, sha, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: contentType}); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

// Get downloads the bytes under their sha256.
func (b *MinioBytes) Get(ctx context.Context, sha string) ([]byte, error) {
	data, err := objects.Download(ctx, b.client, b.bucket, sha)
	if err != nil {
		if errors.Is(err, objects.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return data, nil
}

// Delete removes the object under its sha256.
func (b *MinioBytes) Delete(ctx context.Context, sha string) error {
	if err := b.client.RemoveObject(ctx, b.bucket, sha, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

// List walks the whole bucket, in key order, with each object's modified
// time — the sweep's view of what is there.
func (b *MinioBytes) List(ctx context.Context) ([]StoredBytes, error) {
	out := make([]StoredBytes, 0)
	for obj := range b.client.ListObjects(ctx, b.bucket, minio.ListObjectsOptions{}) {
		if obj.Err != nil {
			if errors.Is(obj.Err, context.Canceled) {
				return nil, context.Canceled
			}
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, obj.Err)
		}
		out = append(out, StoredBytes{Key: obj.Key, Size: obj.Size, Modified: obj.LastModified})
	}
	return out, nil
}

// PostgresNames is the NameDB on the store's own Postgres. The schema is
// the store's and is migrated on start; while Postgres is unreachable
// every operation answers ErrUnavailable (a 503), exactly like a broken
// MinIO does — the store does not crash, and the schema is retried on
// every operation, so it recovers when Postgres comes back.
type PostgresNames struct {
	dsn string

	mu       sync.Mutex
	db       *sql.DB
	migrated bool
}

// NewPostgresNames builds the name table from DATABASE_URL. No attempt
// is made to reach Postgres here: it may be down at start, and that is a
// 503 on the routes, not a failed start.
func NewPostgresNames(ctx context.Context) *PostgresNames {
	return &PostgresNames{dsn: os.Getenv("DATABASE_URL")}
}

// ensureSchema opens the connection and creates the table once. It is
// retried by every operation, so a Postgres that is down at start (or
// comes back after being away) is no obstacle: while it is unreachable
// the store simply answers ErrUnavailable.
func (p *PostgresNames) ensureSchema(ctx context.Context) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.migrated {
		return true
	}
	if p.dsn == "" {
		return false
	}
	db, err := sql.Open("pgx", p.dsn)
	if err != nil {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(cctx, schemaDDL); err != nil {
		_ = db.Close()
		return false
	}
	db.SetMaxOpenConns(8)
	p.db = db
	p.migrated = true
	return true
}

// EnsureSchema runs the migration now, best effort. NewMinioStore calls
// it on start; an error is a 503 on the routes, not a failed start.
func (p *PostgresNames) EnsureSchema(ctx context.Context) error {
	if p.ensureSchema(ctx) {
		return nil
	}
	return ErrUnavailable
}

// schemaDDL is the store's own schema (CONTRACT.md section 2: "its own
// Postgres: which name points to which bytes; schema migrated on start").
const schemaDDL = `
CREATE TABLE IF NOT EXISTS store_files (
    space        text        NOT NULL,
    name         text        NOT NULL,
    sha256       text        NOT NULL,
    size         bigint      NOT NULL,
    content_type text        NOT NULL,
    created_by   text,
    updated_at   timestamptz NOT NULL,
    PRIMARY KEY (space, name)
)`

// Upsert stores the row, replacing the previous row under the same
// name. created_by is stored as NULL when empty, so a GET hands back
// null (CONTRACT.md section 3).
func (p *PostgresNames) Upsert(ctx context.Context, file NamedFile) error {
	if !p.ensureSchema(ctx) {
		return ErrUnavailable
	}
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO store_files (space, name, sha256, size, content_type, created_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7)
		ON CONFLICT (space, name) DO UPDATE SET
		    sha256 = EXCLUDED.sha256,
		    size = EXCLUDED.size,
		    content_type = EXCLUDED.content_type,
		    created_by = EXCLUDED.created_by,
		    updated_at = EXCLUDED.updated_at`,
		file.Space, file.Name, file.Sha256, file.Size, file.ContentType, file.CreatedBy, file.UpdatedAt)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

// Get returns the row under space/name; ok is false when there is no
// such name.
func (p *PostgresNames) Get(ctx context.Context, space, name string) (NamedFile, bool, error) {
	if !p.ensureSchema(ctx) {
		return NamedFile{}, false, ErrUnavailable
	}
	var (
		file      NamedFile
		createdBy sql.NullString
	)
	err := p.db.QueryRowContext(ctx, `
		SELECT sha256, size, content_type, created_by, updated_at
		FROM store_files
		WHERE space = $1 AND name = $2`, space, name).
		Scan(&file.Sha256, &file.Size, &file.ContentType, &createdBy, &file.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return NamedFile{}, false, nil
	}
	if err != nil {
		return NamedFile{}, false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	file.Space, file.Name, file.CreatedBy = space, name, createdBy.String
	return file, true, nil
}

// List returns the rows of space whose name starts with namePrefix (all
// of them when it is empty), in name order. The prefix is matched with
// substr, not LIKE: a name may hold "_", which LIKE would read as a
// wildcard.
func (p *PostgresNames) List(ctx context.Context, space, namePrefix string) ([]NamedFile, error) {
	if !p.ensureSchema(ctx) {
		return nil, ErrUnavailable
	}
	rows, err := p.db.QueryContext(ctx, `
		SELECT name, sha256, size, content_type, created_by, updated_at
		FROM store_files
		WHERE space = $1
		  AND (length($2) = 0 OR substr(name, 1, length($2)) = $2)
		ORDER BY name`, space, namePrefix)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]NamedFile, 0)
	for rows.Next() {
		var (
			file      NamedFile
			createdBy sql.NullString
		)
		if err := rows.Scan(&file.Name, &file.Sha256, &file.Size, &file.ContentType, &createdBy, &file.UpdatedAt); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		file.Space, file.CreatedBy = space, createdBy.String
		out = append(out, file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return out, nil
}

// Delete removes the row and reports whether it was there; it moves the
// name only.
func (p *PostgresNames) Delete(ctx context.Context, space, name string) (bool, error) {
	if !p.ensureSchema(ctx) {
		return false, ErrUnavailable
	}
	res, err := p.db.ExecContext(ctx, `
		DELETE FROM store_files
		WHERE space = $1 AND name = $2`, space, name)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return n > 0, nil
}

// Expire removes the rows of space whose last write is before
// olderThan, in one statement; it is the "a file not written for that
// long is removed" step of a retention rule (CONTRACT.md section 5).
func (p *PostgresNames) Expire(ctx context.Context, space string, olderThan time.Time) error {
	if !p.ensureSchema(ctx) {
		return ErrUnavailable
	}
	if _, err := p.db.ExecContext(ctx, `
		DELETE FROM store_files
		WHERE space = $1
		  AND updated_at < $2`, space, olderThan); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

// Referenced returns the set of sha256 some name points to.
func (p *PostgresNames) Referenced(ctx context.Context) (map[string]bool, error) {
	if !p.ensureSchema(ctx) {
		return nil, ErrUnavailable
	}
	rows, err := p.db.QueryContext(ctx, `SELECT DISTINCT sha256 FROM store_files`)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]bool{}
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		out[sha] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return out, nil
}
