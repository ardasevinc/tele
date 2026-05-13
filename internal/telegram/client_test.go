package telegram

import "testing"

func TestParseAPIID(t *testing.T) {
	id, err := ParseAPIID("123")
	if err != nil {
		t.Fatal(err)
	}
	if id != 123 {
		t.Fatalf("id = %d, want 123", id)
	}
	if _, err := ParseAPIID("0"); err == nil {
		t.Fatal("ParseAPIID accepted zero")
	}
	if _, err := ParseAPIID("nope"); err == nil {
		t.Fatal("ParseAPIID accepted non-number")
	}
}
