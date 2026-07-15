package output

import (
	"encoding/json"
	"errors"
	"testing"
)

func FuzzErrorJSONConversion(f *testing.F) {
	for _, seed := range []string{"boom", "peer x not in cache", "\x1b[31mhostile\x1b[0m", "\xff", "--timeout must not be negative"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, message string) {
		response := ErrorFrom(errors.New(message))
		if response.Error.Message != message {
			t.Fatalf("message changed: got %q, want %q", response.Error.Message, message)
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(encoded) {
			t.Fatalf("invalid JSON: %q", encoded)
		}
	})
}
