package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	hashTime    uint32 = 1
	hashMemory  uint32 = 64 * 1024
	hashThreads uint8  = 4
	hashKeyLen  uint32 = 32
	saltLen            = 16
)

var (
	ErrMalformedHash = errors.New("malformed password hash")

	ErrUnsupportedVersion = errors.New("unsupported argon2 version")
)

func Hash(plain string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}

	key := argon2.IDKey([]byte(plain), salt, hashTime, hashMemory, hashThreads, hashKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, hashMemory, hashTime, hashThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func Verify(plain, encoded string) (bool, error) {
	params, salt, want, err := decode(encoded)
	if err != nil {
		return false, err
	}

	got := argon2.IDKey([]byte(plain), salt, params.time, params.memory, params.threads, uint32(len(want)))

	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func DummyVerify(plain string) {
	salt := make([]byte, saltLen)
	_ = argon2.IDKey([]byte(plain), salt, hashTime, hashMemory, hashThreads, hashKeyLen)
}

type params struct {
	memory  uint32
	time    uint32
	threads uint8
}

func decode(encoded string) (params, []byte, []byte, error) {

	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return params{}, nil, nil, ErrMalformedHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return params{}, nil, nil, ErrMalformedHash
	}
	if version != argon2.Version {
		return params{}, nil, nil, ErrUnsupportedVersion
	}

	var p params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return params{}, nil, nil, ErrMalformedHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return params{}, nil, nil, ErrMalformedHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return params{}, nil, nil, ErrMalformedHash
	}

	return p, salt, key, nil
}
