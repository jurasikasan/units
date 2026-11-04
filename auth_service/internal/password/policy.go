package password

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

const (
	MinLength = 10
	MaxLength = 128
)

var (
	ErrTooShort = fmt.Errorf("password must be at least %d characters", MinLength)
	ErrTooLong  = fmt.Errorf("password must be at most %d characters", MaxLength)
)

func Validate(plain string) error {
	n := utf8.RuneCountInString(plain)
	if n < MinLength {
		return ErrTooShort
	}

	if n > MaxLength {
		return ErrTooLong
	}
	return nil
}

func IsPolicyError(err error) bool {
	return errors.Is(err, ErrTooShort) || errors.Is(err, ErrTooLong)
}
