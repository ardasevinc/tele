package telegram

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	cursorVersion     = 1
	telegramPageSize  = 100
	maxMessageResults = 1000
	maxDialogResults  = 500
)

type RetrievalReceipt struct {
	RequestedCount int    `json:"requested_count"`
	ReturnedCount  int    `json:"returned_count"`
	Complete       *bool  `json:"complete"`
	Truncated      bool   `json:"truncated"`
	NextCursor     string `json:"next_cursor,omitempty"`
	InputCursor    string `json:"input_cursor,omitempty"`
	ServerTotal    *int   `json:"server_total,omitempty"`
	Pages          int    `json:"pages"`
}

type MessagePage struct {
	Items   []Message
	Receipt RetrievalReceipt
}

type ChatPage struct {
	Items   []Chat
	Receipt RetrievalReceipt
}

type retrievalCursor struct {
	Version       int    `json:"v"`
	Kind          string `json:"kind"`
	Scope         string `json:"scope"`
	OffsetID      int    `json:"offset_id,omitempty"`
	OffsetDate    int    `json:"offset_date,omitempty"`
	OffsetRate    int    `json:"offset_rate,omitempty"`
	OffsetPeerRef string `json:"offset_peer_ref,omitempty"`
}

func encodeCursor(cursor retrievalCursor) (string, error) {
	cursor.Version = cursorVersion
	b, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeCursor(value, kind, scope string) (retrievalCursor, error) {
	if strings.TrimSpace(value) == "" {
		return retrievalCursor{Version: cursorVersion, Kind: kind, Scope: scope}, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return retrievalCursor{}, fmt.Errorf("invalid cursor encoding")
	}
	var cursor retrievalCursor
	if err := json.Unmarshal(b, &cursor); err != nil {
		return retrievalCursor{}, fmt.Errorf("invalid cursor payload")
	}
	if cursor.Version != cursorVersion {
		return retrievalCursor{}, fmt.Errorf("unsupported cursor version %d", cursor.Version)
	}
	if cursor.Kind != kind || cursor.Scope != scope {
		return retrievalCursor{}, fmt.Errorf("cursor does not match this %s scope", kind)
	}
	return cursor, nil
}

func scopeFingerprint(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func validateRequestedLimit(limit, max int) error {
	if limit <= 0 {
		return fmt.Errorf("limit must be positive")
	}
	if limit > max {
		return fmt.Errorf("limit %d exceeds the conservative maximum of %d", limit, max)
	}
	return nil
}
