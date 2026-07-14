package telegram

import (
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

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
