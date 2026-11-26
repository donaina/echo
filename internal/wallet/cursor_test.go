package wallet

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
)

func TestCursorRoundTrips(t *testing.T) {
	at := time.Date(2026, 11, 26, 12, 0, 0, 123456789, time.UTC)
	id := ulid.Make().String()

	encoded := encodeCursor(entryCursor{At: at, ID: id})
	got, err := decodeCursor(encoded)
	if err != nil {
		t.Fatalf("decodeCursor: %v", err)
	}
	if !got.At.Equal(at) || got.ID != id {
		t.Fatalf("cursor = %+v, want %+v", got, entryCursor{At: at, ID: id})
	}
}

func TestCursorRejectsAnythingItWouldNotWrite(t *testing.T) {
	at := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	id := ulid.Make().String()
	raw := at.Format(time.RFC3339Nano) + "|" + id

	cases := map[string]string{
		"not base64":    "!!!",
		"no separator":  base64.RawURLEncoding.EncodeToString([]byte(id)),
		"bad time":      base64.RawURLEncoding.EncodeToString([]byte("yesterday|" + id)),
		"bad id":        base64.RawURLEncoding.EncodeToString([]byte(at.Format(time.RFC3339Nano) + "|not-a-ulid")),
		"empty id":      base64.RawURLEncoding.EncodeToString([]byte(at.Format(time.RFC3339Nano) + "|")),
		"non-canonical": base64.StdEncoding.EncodeToString([]byte(raw)),
	}
	for name, cursor := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCursor(cursor); err == nil {
				t.Fatalf("accepted %q", cursor)
			}
		})
	}
}
