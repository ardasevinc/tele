package telegram

import (
	"strings"
	"testing"
)

func TestRedactSensitiveText(t *testing.T) {
	input := "Login code: 12345. Do not give this code to anyone.\nThis is your login code:\nABCDE123"
	got := redactSensitiveText(input)
	if strings.Contains(got, "12345") || strings.Contains(got, "ABCDE123") {
		t.Fatalf("redaction leaked code: %q", got)
	}
	if !strings.Contains(got, "Login code: [redacted]") {
		t.Fatalf("missing numeric redaction: %q", got)
	}
	if !strings.Contains(got, "This is your login code:\n[redacted]") {
		t.Fatalf("missing web redaction: %q", got)
	}
}

func TestRedactMessageTextOnlyProtectsTelegramLoginCodes(t *testing.T) {
	input := "Login code: 12345. Do not give this code to anyone."
	got, redactions := redactMessageText("user:777000", input)
	if strings.Contains(got, "12345") || len(redactions) != 1 || redactions[0] != "telegram_login_code" {
		t.Fatalf("service redaction = %q %#v", got, redactions)
	}
	got, redactions = redactMessageText("user:42", input)
	if got != input || redactions != nil {
		t.Fatalf("ordinary message changed = %q %#v", got, redactions)
	}
}
