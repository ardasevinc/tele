package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/secrets"
	telesession "github.com/ardasevinc/tele/internal/session"
)

type testSecretStore struct {
	values map[string][]byte
}

func (s *testSecretStore) Get(_ context.Context, _, key string) ([]byte, error) {
	value, ok := s.values[key]
	if !ok {
		return nil, secrets.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (s *testSecretStore) Set(_ context.Context, _, key string, value []byte) error {
	s.values[key] = append([]byte(nil), value...)
	return nil
}

func (s *testSecretStore) Delete(_ context.Context, _, key string) error {
	delete(s.values, key)
	return nil
}

func TestPendingAuthValidation(t *testing.T) {
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	valid := fmt.Sprintf(`{"phone":"+90555","phone_code_hash":"hash","created_at":%q}`, now.Add(-pendingAuthTTL+time.Second).Format(time.RFC3339))
	if _, err := parsePendingAuth([]byte(valid), now); err != nil {
		t.Fatalf("valid pending auth: %v", err)
	}

	expired := fmt.Sprintf(`{"phone":"+90555","phone_code_hash":"hash","created_at":%q}`, now.Add(-pendingAuthTTL-time.Second).Format(time.RFC3339))
	if _, err := parsePendingAuth([]byte(expired), now); !errors.Is(err, ErrPendingAuthExpired) {
		t.Fatalf("expired error = %v", err)
	}
	for _, invalid := range []string{"{", `{}`, `{"phone":"+90555","phone_code_hash":"hash","created_at":"nope"}`} {
		if _, err := parsePendingAuth([]byte(invalid), now); !errors.Is(err, ErrPendingAuthInvalid) {
			t.Fatalf("invalid %q error = %v", invalid, err)
		}
	}
}

func TestPendingAuthDeletesExpiredState(t *testing.T) {
	store := &testSecretStore{values: map[string][]byte{}}
	store.values[authPendingKey] = []byte(`{"phone":"+90555","phone_code_hash":"hash","created_at":"2000-01-01T00:00:00Z"}`)
	app := App{Profile: "main", Secrets: store}
	if _, err := app.pendingAuth(context.Background()); !errors.Is(err, ErrPendingAuthExpired) {
		t.Fatalf("pendingAuth error = %v", err)
	}
	if _, ok := store.values[authPendingKey]; ok {
		t.Fatal("expired pending auth was retained")
	}
}

func TestResetLocalAuthDeletesEveryLocalAuthArtifact(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "main", "session.enc")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionPath, []byte("ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &testSecretStore{values: map[string][]byte{
		authPendingKey:            []byte("pending"),
		telesession.Key:           []byte("legacy"),
		telesession.EncryptionKey: []byte("key"),
		apiHashKey:                []byte("keep-me"),
	}}
	app := App{Profile: "main", Paths: config.Paths{Data: dir}, Secrets: store}
	if err := app.ResetLocalAuth(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sessionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session still exists: %v", err)
	}
	for _, key := range []string{authPendingKey, telesession.Key, telesession.EncryptionKey} {
		if _, ok := store.values[key]; ok {
			t.Fatalf("auth artifact %q retained", key)
		}
	}
	if string(store.values[apiHashKey]) != "keep-me" {
		t.Fatal("API hash was unexpectedly deleted")
	}
}

func TestInteractiveAuthUsesOneConfiguredBufferedInput(t *testing.T) {
	authenticator := newInteractiveAuth(strings.NewReader("+90555\n12345\nsecret\n"), io.Discard, LoginOptions{})
	phone, err := authenticator.Phone(context.Background())
	if err != nil || phone != "+90555" {
		t.Fatalf("phone = %q, err = %v", phone, err)
	}
	code, err := authenticator.Code(context.Background(), nil)
	if err != nil || code != "12345" {
		t.Fatalf("code = %q, err = %v", code, err)
	}
	password, err := authenticator.Password(context.Background())
	if err != nil || password != "secret" {
		t.Fatalf("password = %q, err = %v", password, err)
	}
}

func TestInteractiveAuthPromptHonorsCancellation(t *testing.T) {
	reader, writer := io.Pipe()
	authenticator := newInteractiveAuth(reader, io.Discard, LoginOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := authenticator.Code(ctx, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Code error = %v, want deadline exceeded", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

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

func TestSafeDownloadFileName(t *testing.T) {
	got := safeDownloadFileName(42, "../weird:name.jpg")
	if got != "42-weird-name.jpg" {
		t.Fatalf("safeDownloadFileName = %q, want %q", got, "42-weird-name.jpg")
	}
}

func TestAtomicDownloadPromotesOnlyCompletePrivateFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "media.bin")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := errors.New("download interrupted")
	_, err := atomicDownload(path, func(w io.WriterAt) (tg.StorageFileTypeClass, error) {
		_, _ = w.WriteAt([]byte("partial"), 0)
		return nil, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	if got := string(mustReadFile(t, path)); got != "original" {
		t.Fatalf("failed download replaced target with %q", got)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, ".tele-tmp-*")); len(matches) != 0 {
		t.Fatalf("partial files remain: %v", matches)
	}

	if _, err := atomicDownload(path, func(w io.WriterAt) (tg.StorageFileTypeClass, error) {
		_, err := w.WriteAt([]byte("complete"), 0)
		return nil, err
	}); err != nil {
		t.Fatal(err)
	}
	if got := string(mustReadFile(t, path)); got != "complete" {
		t.Fatalf("completed download = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("download mode = %04o", got)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPlanDelete(t *testing.T) {
	tests := []struct {
		name       string
		input      tg.InputPeerClass
		scope      DeleteScope
		wantRoute  deleteRoute
		wantRevoke bool
		wantErr    string
	}{
		{name: "user for me", input: &tg.InputPeerUser{}, scope: DeleteScopeForMe, wantRoute: deleteRouteMessages},
		{name: "user revoke", input: &tg.InputPeerUser{}, scope: DeleteScopeRevoke, wantRoute: deleteRouteMessages, wantRevoke: true},
		{name: "basic group for me", input: &tg.InputPeerChat{}, scope: DeleteScopeForMe, wantRoute: deleteRouteMessages},
		{name: "basic group revoke", input: &tg.InputPeerChat{}, scope: DeleteScopeRevoke, wantRoute: deleteRouteMessages, wantRevoke: true},
		{name: "cached channel for me rejected", input: &tg.InputPeerChannel{ChannelID: 42, AccessHash: 7}, scope: DeleteScopeForMe, wantErr: "only be deleted with --revoke --yes"},
		{name: "cached supergroup for me rejected", input: &tg.InputPeerChannel{ChannelID: 42, AccessHash: 7}, scope: DeleteScopeForMe, wantErr: "only be deleted with --revoke --yes"},
		{name: "username resolved channel for me rejected", input: &tg.InputPeerChannel{ChannelID: 84, AccessHash: 9}, scope: DeleteScopeForMe, wantErr: "only be deleted with --revoke --yes"},
		{name: "channel revoke", input: &tg.InputPeerChannel{ChannelID: 42, AccessHash: 7}, scope: DeleteScopeRevoke, wantRoute: deleteRouteChannels},
		{name: "unknown scope", input: &tg.InputPeerUser{}, scope: DeleteScope("unknown"), wantErr: "unsupported delete scope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := planDelete(tt.input, tt.scope)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("planDelete error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Route != tt.wantRoute || got.Revoke != tt.wantRevoke {
				t.Fatalf("planDelete = %+v, want route %q revoke %t", got, tt.wantRoute, tt.wantRevoke)
			}
			if tt.wantRoute == deleteRouteChannels {
				if got.Channel == nil || got.Channel.ChannelID != 42 || got.Channel.AccessHash != 7 {
					t.Fatalf("planDelete channel = %+v, want id 42 hash 7", got.Channel)
				}
			}
		})
	}
}

func TestMutationFailureOutcomes(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		dispatched    bool
		wantOutcome   MutationOutcome
		wantRetrySafe bool
	}{
		{name: "pre-dispatch rejected", err: errors.New("resolve failed"), wantOutcome: MutationRejected, wantRetrySafe: true},
		{name: "telegram rejected", err: tgerr.New(400, "MESSAGE_ID_INVALID"), dispatched: true, wantOutcome: MutationRejected, wantRetrySafe: true},
		{name: "post-dispatch timeout unknown", err: context.DeadlineExceeded, dispatched: true, wantOutcome: MutationOutcomeUnknown},
		{name: "post-dispatch transport unknown", err: errors.New("connection reset"), dispatched: true, wantOutcome: MutationOutcomeUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := mutationFailure(tt.err, tt.dispatched, "handle:1")
			var mutationErr MutationError
			if !errors.As(err, &mutationErr) {
				t.Fatalf("mutationFailure returned %T, want MutationError", err)
			}
			if mutationErr.Outcome != tt.wantOutcome || mutationErr.RetrySafe != tt.wantRetrySafe {
				t.Fatalf("mutationFailure = %+v, want outcome %q retry_safe %t", mutationErr, tt.wantOutcome, tt.wantRetrySafe)
			}
		})
	}
}

func TestValidateMutationPreview(t *testing.T) {
	if err := validateMutationPreview("send", &tg.InputPeerUser{}, ""); err != nil {
		t.Fatal(err)
	}
	if err := validateMutationPreview("delete", &tg.InputPeerChannel{}, DeleteScopeForMe); err == nil {
		t.Fatal("validateMutationPreview accepted channel --for-me")
	}
	if err := validateMutationPreview("unknown", &tg.InputPeerUser{}, ""); err == nil {
		t.Fatal("validateMutationPreview accepted unknown action")
	}
}

func TestMutationResultConfirmedWithoutMessageID(t *testing.T) {
	got := mutationResult("send", "user:1", 0, "random_id:42")
	if !got.OK || got.Outcome != MutationConfirmed || got.RetrySafe || got.MessageID != 0 || got.ReconciliationHandle != "random_id:42" {
		t.Fatalf("mutationResult = %+v", got)
	}
}

func TestConfirmedMutationOutputError(t *testing.T) {
	err := ConfirmedMutationOutputError(MutationResult{ReconciliationHandle: "random_id:42"}, errors.New("broken pipe"))
	var mutationErr MutationError
	if !errors.As(err, &mutationErr) {
		t.Fatalf("ConfirmedMutationOutputError returned %T", err)
	}
	if mutationErr.Outcome != MutationConfirmed || mutationErr.RetrySafe || mutationErr.ReconciliationHandle != "random_id:42" {
		t.Fatalf("ConfirmedMutationOutputError = %+v", mutationErr)
	}
}

func TestChatsFromDialogsKeepsSameIDPreviewsScopedToPeer(t *testing.T) {
	user1 := &tg.User{ID: 1, AccessHash: 10, FirstName: "One"}
	user1.SetFlags()
	user2 := &tg.User{ID: 2, AccessHash: 20, FirstName: "Two"}
	user2.SetFlags()
	res := &tg.MessagesDialogsSlice{
		Dialogs: []tg.DialogClass{
			&tg.Dialog{Peer: &tg.PeerUser{UserID: 1}, TopMessage: 5},
			&tg.Dialog{Peer: &tg.PeerUser{UserID: 2}, TopMessage: 5},
		},
		Messages: []tg.MessageClass{
			&tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: 1}, Message: "one"},
			&tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: 2}, Message: "two"},
		},
		Users: []tg.UserClass{user1, user2},
	}
	chats, _ := chatsFromDialogs(res)
	if len(chats) != 2 || chats[0].LastMessagePreview != "one" || chats[1].LastMessagePreview != "two" {
		t.Fatalf("chatsFromDialogs previews = %+v", chats)
	}
}
