package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

func TestCursorRoundTrip(t *testing.T) {
	scope := scopeFingerprint("history", "user:1", "after:0")
	want := retrievalCursor{
		Kind:          "history",
		Scope:         scope,
		OffsetID:      42,
		OffsetDate:    123,
		OffsetRate:    7,
		OffsetPeerRef: "user:1",
	}
	encoded, err := encodeCursor(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeCursor(encoded, "history", scope)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != cursorVersion || got.Kind != want.Kind || got.Scope != want.Scope || got.OffsetID != want.OffsetID || got.OffsetDate != want.OffsetDate || got.OffsetRate != want.OffsetRate || got.OffsetPeerRef != want.OffsetPeerRef {
		t.Fatalf("decoded cursor = %+v, want %+v", got, want)
	}
}

func TestCursorRejectsWrongScopeAndKind(t *testing.T) {
	encoded, err := encodeCursor(retrievalCursor{Kind: "history", Scope: "scope-a", OffsetID: 42})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		kind  string
		scope string
	}{
		{kind: "search", scope: "scope-a"},
		{kind: "history", scope: "scope-b"},
	} {
		if _, err := decodeCursor(encoded, test.kind, test.scope); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("decodeCursor(%q, %q) error = %v", test.kind, test.scope, err)
		}
	}
}

func TestCursorRejectsMalformedInput(t *testing.T) {
	if _, err := decodeCursor("not-base64!", "history", "scope"); err == nil {
		t.Fatal("decodeCursor accepted malformed input")
	}
}

func TestValidateRequestedLimit(t *testing.T) {
	if err := validateRequestedLimit(1000, maxMessageResults); err != nil {
		t.Fatal(err)
	}
	if err := validateRequestedLimit(1001, maxMessageResults); err == nil {
		t.Fatal("validateRequestedLimit accepted an over-limit request")
	}
}

func TestReadPagesHonorsLimitAcrossTelegramPages(t *testing.T) {
	requests := 0
	fetch := fakeHistory(1200, func(req *tg.MessagesGetHistoryRequest) {
		requests++
		if req.Limit > telegramPageSize {
			t.Fatalf("Telegram request limit = %d, want <= %d", req.Limit, telegramPageSize)
		}
	})
	page, err := readPages(context.Background(), &tg.InputPeerUser{}, "user:1", ReadOptions{Limit: 1000}, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1000 || requests != 10 {
		t.Fatalf("readPages returned %d items in %d requests, want 1000 in 10", len(page.Items), requests)
	}
	if page.Items[0].ID != 1200 || page.Items[999].ID != 201 || !page.Receipt.Truncated || page.Receipt.NextCursor == "" {
		t.Fatalf("readPages receipt/items = first %d last %d receipt %+v", page.Items[0].ID, page.Items[999].ID, page.Receipt)
	}
}

func TestReadPagesProvesExhaustion(t *testing.T) {
	page, err := readPages(context.Background(), &tg.InputPeerUser{}, "user:1", ReadOptions{Limit: 1000}, fakeHistory(250, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 250 || page.Receipt.Complete == nil || !*page.Receipt.Complete || page.Receipt.Truncated || page.Receipt.Pages != 3 {
		t.Fatalf("readPages = %d items, receipt %+v", len(page.Items), page.Receipt)
	}
}

func TestReadPagesScansUntilSinceBoundary(t *testing.T) {
	since := time.Unix(151, 0).UTC()
	page, err := readPages(context.Background(), &tg.InputPeerUser{}, "user:1", ReadOptions{Limit: 200, Since: since}, fakeHistory(300, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 150 || page.Items[len(page.Items)-1].ID != 151 || page.Receipt.Complete == nil || !*page.Receipt.Complete || page.Receipt.Pages != 2 {
		t.Fatalf("readPages window = %d items, last %+v, receipt %+v", len(page.Items), page.Items[len(page.Items)-1], page.Receipt)
	}
}

func TestReadPagesCursorStableAcrossEqualTimestamps(t *testing.T) {
	fetch := fakeHistoryWithDate(120, 1000, nil)
	first, err := readPages(context.Background(), &tg.InputPeerUser{}, "user:1", ReadOptions{Limit: 100}, fetch)
	if err != nil {
		t.Fatal(err)
	}
	second, err := readPages(context.Background(), &tg.InputPeerUser{}, "user:1", ReadOptions{Limit: 20, Cursor: first.Receipt.NextCursor}, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if first.Items[99].ID != 21 || len(second.Items) != 20 || second.Items[0].ID != 20 || second.Items[19].ID != 1 {
		t.Fatalf("cursor pages overlap or gap: first last %d, second first/last %d/%d", first.Items[99].ID, second.Items[0].ID, second.Items[19].ID)
	}
}

func TestReadPagesAcceptsDeletedCursorAnchor(t *testing.T) {
	opts := ReadOptions{Limit: 10}
	scope := readScope("user:1", opts)
	cursor, err := encodeCursor(retrievalCursor{Kind: "history", Scope: scope, OffsetID: 90})
	if err != nil {
		t.Fatal(err)
	}
	opts.Cursor = cursor
	page, err := readPages(context.Background(), &tg.InputPeerUser{}, "user:1", opts, fakeHistory(100, nil))
	if err != nil {
		t.Fatal(err)
	}
	if page.Items[0].ID != 89 {
		t.Fatalf("first item after deleted anchor = %d, want 89", page.Items[0].ID)
	}
}

func TestReadPagesAroundIsExplicitlyUnknown(t *testing.T) {
	page, err := readPages(context.Background(), &tg.InputPeerUser{}, "user:1", ReadOptions{Limit: 20, AroundID: 50}, fakeHistory(100, nil))
	if err != nil {
		t.Fatal(err)
	}
	if page.Receipt.Complete != nil || !page.Receipt.Truncated || page.Receipt.NextCursor != "" {
		t.Fatalf("around receipt = %+v", page.Receipt)
	}
}

func TestSearchPagesBuildsStableGlobalCursor(t *testing.T) {
	fetch := fakeSearch(120)
	first, err := searchPages(context.Background(), "", SearchOptions{Query: "hello", Limit: 100}, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 100 || first.Items[0].SourcePeerRef != "user:10" || first.Receipt.NextCursor == "" {
		t.Fatalf("first global search page = %d items, receipt %+v", len(first.Items), first.Receipt)
	}
	cursor, err := decodeCursor(first.Receipt.NextCursor, "search-global", scopeFingerprint("search-global", "", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if cursor.OffsetID != 21 || cursor.OffsetPeerRef != "user:10" || cursor.OffsetRate != 77 {
		t.Fatalf("global cursor = %+v", cursor)
	}
	second, err := searchPages(context.Background(), "", SearchOptions{Query: "hello", Limit: 20, Cursor: first.Receipt.NextCursor}, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 20 || second.Items[0].ID != 20 || second.Items[19].ID != 1 {
		t.Fatalf("second global page = %+v", second.Items)
	}
}

func TestSearchCursorIsQueryAndScopeBound(t *testing.T) {
	first, err := searchPages(context.Background(), "user:10", SearchOptions{Query: "hello", Limit: 10}, fakeSearch(20))
	if err != nil {
		t.Fatal(err)
	}
	for _, opts := range []SearchOptions{
		{Query: "different", Limit: 10, Cursor: first.Receipt.NextCursor},
		{Query: "hello", Peer: "user:11", Limit: 10, Cursor: first.Receipt.NextCursor},
	} {
		scopePeer := "user:10"
		if opts.Peer != "" {
			scopePeer = opts.Peer
		}
		if _, err := searchPages(context.Background(), scopePeer, opts, fakeSearch(20)); err == nil {
			t.Fatalf("searchPages accepted mismatched cursor for %+v", opts)
		}
	}
}

func TestDialogPagesHonorsLimitAndCursor(t *testing.T) {
	first, err := dialogPages(context.Background(), ChatOptions{Limit: 100}, "", fakeDialogs(120))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 100 || first.Items[0].Ref != "user:120" || first.Items[99].Ref != "user:21" || first.Receipt.NextCursor == "" {
		t.Fatalf("first dialog page = %d items, receipt %+v", len(first.Items), first.Receipt)
	}
	second, err := dialogPages(context.Background(), ChatOptions{Limit: 20, Cursor: first.Receipt.NextCursor}, "", fakeDialogs(120))
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 20 || second.Items[0].Ref != "user:20" || second.Items[19].Ref != "user:1" {
		t.Fatalf("second dialog page = %+v", second.Items)
	}
}

func TestDialogPagesFiltersAcrossRawDialogs(t *testing.T) {
	page, err := dialogPages(context.Background(), ChatOptions{Limit: 5}, "unread", fakeDialogs(120))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 5 || page.Items[0].Ref != "user:120" || page.Items[4].Ref != "user:80" || page.Receipt.Pages != 1 || page.Receipt.NextCursor == "" {
		t.Fatalf("unread dialog page = %+v, receipt %+v", page.Items, page.Receipt)
	}
}

func TestDialogCursorIsModeBound(t *testing.T) {
	page, err := dialogPages(context.Background(), ChatOptions{Limit: 5}, "unread", fakeDialogs(120))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dialogPages(context.Background(), ChatOptions{Limit: 5, Cursor: page.Receipt.NextCursor}, "mentions", fakeDialogs(120)); err == nil {
		t.Fatal("dialogPages accepted unread cursor for mentions")
	}
}

func fakeHistory(total int, observe func(*tg.MessagesGetHistoryRequest)) historyFetcher {
	return fakeHistoryWithDate(total, 0, observe)
}

func fakeHistoryWithDate(total, fixedDate int, observe func(*tg.MessagesGetHistoryRequest)) historyFetcher {
	return func(_ context.Context, req *tg.MessagesGetHistoryRequest) (tg.MessagesMessagesClass, error) {
		if observe != nil {
			observe(req)
		}
		start := total
		if req.OffsetID > 0 {
			start = req.OffsetID - 1
		}
		messages := make([]tg.MessageClass, 0, req.Limit)
		for id := start; id > 0 && len(messages) < req.Limit; id-- {
			date := id
			if fixedDate > 0 {
				date = fixedDate
			}
			messages = append(messages, &tg.Message{ID: id, Date: date, PeerID: &tg.PeerUser{UserID: 1}})
		}
		return &tg.MessagesMessagesSlice{Count: total, Messages: messages}, nil
	}
}

func fakeSearch(total int) searchFetcher {
	return func(_ context.Context, cursor retrievalCursor, limit int) (tg.MessagesMessagesClass, error) {
		start := total
		if cursor.OffsetID > 0 {
			start = cursor.OffsetID - 1
		}
		messages := make([]tg.MessageClass, 0, limit)
		for id := start; id > 0 && len(messages) < limit; id-- {
			messages = append(messages, &tg.Message{ID: id, Date: id, PeerID: &tg.PeerUser{UserID: 10}, Message: "hello"})
		}
		user := &tg.User{ID: 10, AccessHash: 100, FirstName: "Alice"}
		user.SetFlags()
		res := &tg.MessagesMessagesSlice{Count: total, Messages: messages, Users: []tg.UserClass{user}}
		res.SetNextRate(77)
		return res, nil
	}
}

func fakeDialogs(total int) dialogFetcher {
	return func(_ context.Context, cursor retrievalCursor, limit int) (tg.MessagesDialogsClass, error) {
		start := total
		if cursor.OffsetID > 0 {
			start = cursor.OffsetID - 1
		}
		dialogs := make([]tg.DialogClass, 0, limit)
		messages := make([]tg.MessageClass, 0, limit)
		users := make([]tg.UserClass, 0, limit)
		for id := start; id > 0 && len(dialogs) < limit; id-- {
			unread := 0
			if id%10 == 0 {
				unread = 1
			}
			dialogs = append(dialogs, &tg.Dialog{Peer: &tg.PeerUser{UserID: int64(id)}, TopMessage: id, UnreadCount: unread})
			messages = append(messages, &tg.Message{ID: id, Date: id, PeerID: &tg.PeerUser{UserID: int64(id)}, Message: "preview"})
			user := &tg.User{ID: int64(id), AccessHash: int64(id * 10), FirstName: "User"}
			user.SetFlags()
			users = append(users, user)
		}
		return &tg.MessagesDialogsSlice{Count: total, Dialogs: dialogs, Messages: messages, Users: users}, nil
	}
}
