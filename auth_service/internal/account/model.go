package account

import (
	"time"

	"github.com/google/uuid"
)

type Account struct {
	ID        uuid.UUID `json:"id"`
	Username  string    `json:"username"`
	Email     *string   `json:"email,omitempty"`
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"created_at"`

	passwordHash string
}
