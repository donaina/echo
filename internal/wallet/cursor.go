package wallet

import (
	"encoding/base64"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	apperr "github.com/donaina/echo/internal/platform/errors"
)

// entryCursor is the position of the last entry on the previous page. The
// tuple is (created_at, id) because timestamps alone are not unique: a bulk
// load can post many entries in one instant.
type entryCursor struct {
	At time.Time
	ID string
}

func encodeCursor(c entryCursor) string {
	raw := c.At.UTC().Format(time.RFC3339Nano) + "|" + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (entryCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return entryCursor{}, invalidCursor()
	}
	atRaw, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return entryCursor{}, invalidCursor()
	}
	at, err := time.Parse(time.RFC3339Nano, atRaw)
	if err != nil {
		return entryCursor{}, invalidCursor()
	}
	if _, err := ulid.Parse(id); err != nil {
		return entryCursor{}, invalidCursor()
	}
	c := entryCursor{At: at, ID: id}
	// Only the spelling we would have written is accepted; base64 has more
	// than one encoding for the same bytes.
	if encodeCursor(c) != s {
		return entryCursor{}, invalidCursor()
	}
	return c, nil
}

func invalidCursor() error {
	return apperr.New(apperr.InvalidCursor, "cursor is not valid")
}
