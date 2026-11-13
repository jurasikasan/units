// Package store implements the file routes of CONTRACT.md:
// PUT /v1/{space}/{name}, GET /v1/{space}/{name}, the listing
// GET /v1/{space}?prefix=<p>, DELETE /v1/{space}/{name} and the read by
// content GET /v1/_sha256/{hex}.
//
// The bytes live in MinIO once, under their sha256, and the names —
// which name points to which bytes — live in the store's own Postgres;
// a name never holds a path, so space and name are the file's identity.
package store

import (
	"regexp"
	"time"
)

// Alphabets of CONTRACT.md section 4: the expected values in the tests
// come from these, not from the code.
var (
	spaceRe = regexp.MustCompile(`^[a-z][a-z0-9-]{2,31}$`)
	nameRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,254}$`)
	shaRe   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// SpaceValid and NameValid report whether the identifier keeps to the
// alphabet of section 4.
func SpaceValid(space string) bool { return spaceRe.MatchString(space) }
func NameValid(name string) bool   { return nameRe.MatchString(name) }

// ShaValid reports whether sha is a sha256 the way the store writes it:
// 64 lowercase hex characters.
func ShaValid(sha string) bool { return shaRe.MatchString(sha) }

// ObjectKey is the key a file has in the store: space and name, so a
// name can never be a path (it may not hold a "/").
func ObjectKey(space, name string) string {
	return space + "/" + name
}

// ObjectMeta is the metadata a PUT records with the bytes, so the listing
// route can answer it back without downloading the file.
type ObjectMeta struct {
	Sha256      string
	ContentType string
	CreatedBy   string // the X-Principal-Id as received, or ""
	UpdatedAt   time.Time
}

// Object is one entry a store's List answers: the object's facts as the
// store knows them. The service turns each Object into a File, dropping
// the space from the key.
type Object struct {
	Key         string
	Size        int64
	ContentType string
	Meta        ObjectMeta
}

// File is the metadata of a stored file: the same fields the listing
// route of section 3 answers, as the response of a PUT.
type File struct {
	Name        string  `json:"name"`
	Sha256      string  `json:"sha256"`
	Size        int64   `json:"size"`
	ContentType string  `json:"content_type"`
	CreatedBy   *string `json:"created_by"`
	UpdatedAt   string  `json:"updated_at"`
}
