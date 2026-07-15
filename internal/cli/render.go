package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ardasevinc/tele/internal/output"
	tgapp "github.com/ardasevinc/tele/internal/telegram"
)

func writeValue(s *appState, value any, human func(output.Writer) error) error {
	return writeValueWithMeta(s, value, s.meta(0, "", nil), human)
}

func writeValueWithMeta(s *appState, value any, meta output.Meta, human func(output.Writer) error) error {
	w := s.writer()
	if w.Format == output.JSON {
		return w.JSON(output.NewEnvelope(meta, value))
	}
	if w.Format == output.JSONL {
		return w.JSONL([]any{output.MetaRecord(meta), output.DataRecord(value)})
	}
	return human(w)
}

func writeMutationResult(s *appState, result tgapp.MutationResult, meta output.Meta, receipt string) error {
	err := writeValueWithMeta(s, result, meta, func(w output.Writer) error {
		return w.Print(receipt)
	})
	if err != nil {
		return tgapp.ConfirmedMutationOutputError(result, err)
	}
	return nil
}

func writeMessages(s *appState, messages []tgapp.Message, meta output.Meta) error {
	return writeMessagesWithFormat(s, messages, meta, output.Human)
}

func writeMessagesWithFormat(s *appState, messages []tgapp.Message, meta output.Meta, defaultFormat output.Format) error {
	w := s.writerWithDefault(defaultFormat)
	if w.Format == output.JSON {
		return w.JSON(output.NewEnvelope(meta, messages))
	}
	if w.Format == output.JSONL {
		items := make([]any, 0, len(messages)+1)
		items = append(items, output.MetaRecord(meta))
		for _, msg := range messages {
			items = append(items, output.DataRecord(msg))
		}
		return w.JSONL(items)
	}
	if meta.Retrieval != nil {
		if _, err := fmt.Fprintln(w.Out, retrievalSummary(meta)); err != nil {
			return err
		}
	}
	for _, msg := range messages {
		text := indentHumanText(msg.Text)
		if text == "" {
			text = "[" + safeHuman(firstNonEmpty(msg.Media, msg.Service, "empty")) + "]"
		}
		location := ""
		if msg.SourcePeerRef != "" && (meta.PeerRef == "" || msg.SourcePeerRef != meta.PeerRef) {
			location = safeHuman(firstNonEmpty(msg.SourcePeerLabel, msg.SourcePeerRef)) + " "
		}
		if _, err := fmt.Fprintf(w.Out, "%s %s#%d %s: %s\n", safeHuman(msg.Date), location, msg.ID, messageSpeaker(msg), text); err != nil {
			return err
		}
	}
	return nil
}

func writeTranscript(s *appState, messages []tgapp.Message, meta output.Meta, peer tgapp.PeerInfo) error {
	w := s.writer()
	if w.Format != output.Human {
		return writeMessages(s, messages, meta)
	}
	headerPeer := peer.Ref
	if headerPeer == "" {
		headerPeer = meta.PeerRef
	}
	label := peerLabel(peer)
	if label != "" {
		headerPeer += " (" + label + ")"
	}
	if _, err := fmt.Fprintf(w.Out, "peer: %s\n", safeHuman(headerPeer)); err != nil {
		return err
	}
	if meta.FetchedAt != "" {
		if _, err := fmt.Fprintf(w.Out, "fetched_at: %s\n", safeHuman(meta.FetchedAt)); err != nil {
			return err
		}
	}
	if meta.Limit > 0 {
		if _, err := fmt.Fprintf(w.Out, "limit: %d\n", meta.Limit); err != nil {
			return err
		}
	}
	if meta.Retrieval != nil {
		if _, err := fmt.Fprintf(w.Out, "%s\n", retrievalSummary(meta)); err != nil {
			return err
		}
	}
	if len(meta.SideEffects) > 0 {
		if _, err := fmt.Fprintf(w.Out, "side_effects: %s\n", safeHuman(strings.Join(meta.SideEffects, ", "))); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w.Out, "messages: %d\n\n", len(messages)); err != nil {
		return err
	}
	lastDay := ""
	for _, msg := range messages {
		day, clock := transcriptDateParts(msg.Date)
		if day != "" && day != lastDay {
			if lastDay != "" {
				if _, err := fmt.Fprintln(w.Out); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(w.Out, "-- %s --\n", day); err != nil {
				return err
			}
			lastDay = day
		}
		if clock == "" {
			clock = "??:??"
		}
		speaker := messageSpeaker(msg)
		line := transcriptBody(msg)
		if _, err := fmt.Fprintf(w.Out, "[%d] %s %s: %s\n", msg.ID, clock, speaker, firstTranscriptLine(line)); err != nil {
			return err
		}
		for _, continuation := range transcriptContinuations(line) {
			if _, err := fmt.Fprintf(w.Out, "    %s\n", continuation); err != nil {
				return err
			}
		}
	}
	return nil
}

func retrievalSummary(meta output.Meta) string {
	if meta.Retrieval == nil {
		return "retrieval: unavailable"
	}
	retrieval := meta.Retrieval
	complete := "unknown"
	if retrieval.Complete != nil {
		complete = strconv.FormatBool(*retrieval.Complete)
	}
	parts := []string{
		fmt.Sprintf("requested=%d", retrieval.RequestedCount),
		fmt.Sprintf("returned=%d", retrieval.ReturnedCount),
		"complete=" + complete,
		fmt.Sprintf("truncated=%t", retrieval.Truncated),
		fmt.Sprintf("pages=%d", retrieval.Pages),
	}
	if retrieval.ServerTotal != nil {
		parts = append(parts, fmt.Sprintf("server_total=%d", *retrieval.ServerTotal))
	}
	if retrieval.NextCursor != "" {
		parts = append(parts, "next_cursor="+safeHuman(retrieval.NextCursor))
	}
	return "retrieval: " + strings.Join(parts, " ")
}

func peerLabel(peer tgapp.PeerInfo) string {
	title := strings.TrimSpace(safeHuman(peer.Title))
	username := strings.TrimSpace(safeHuman(peer.Username))
	if username != "" {
		username = "@" + strings.TrimPrefix(username, "@")
	}
	switch {
	case title != "" && username != "":
		return title + " " + username
	case title != "":
		return title
	default:
		return username
	}
}

func transcriptDateParts(value string) (string, string) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", ""
	}
	t = t.UTC()
	return t.Format("2006-01-02"), t.Format("15:04")
}

func transcriptBody(msg tgapp.Message) string {
	text := strings.TrimSpace(safeHuman(msg.Text))
	if text != "" {
		return text
	}
	if msg.Media != "" {
		return "[" + mediaLabel(safeHuman(msg.Media)) + "]"
	}
	if msg.Service != "" {
		return "[service: " + safeHuman(msg.Service) + "]"
	}
	return "[empty]"
}

func messageSpeaker(msg tgapp.Message) string {
	if msg.Outgoing {
		return "me"
	}
	return safeHuman(firstNonEmpty(msg.SenderLabel, msg.SenderPeerRef, "them"))
}

func mediaLabel(media string) string {
	label := strings.TrimPrefix(media, "messageMedia")
	if label == "" {
		return media
	}
	return strings.ToLower(label[:1]) + label[1:]
}

func firstTranscriptLine(value string) string {
	lines := transcriptLines(value)
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

func transcriptContinuations(value string) []string {
	lines := transcriptLines(value)
	if len(lines) <= 1 {
		return nil
	}
	return lines[1:]
}

func transcriptLines(value string) []string {
	raw := strings.Split(strings.TrimSpace(value), "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		lines = append(lines, strings.TrimRight(line, " \t"))
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func accountLabel(a *tgapp.Account) string {
	if a == nil {
		return "unknown"
	}
	name := strings.TrimSpace(safeHuman(a.FirstName + " " + a.LastName))
	if a.Username != "" {
		username := safeHuman(a.Username)
		if name != "" {
			return name + " @" + username
		}
		return "@" + username
	}
	if name != "" {
		return name
	}
	return fmt.Sprintf("%d", a.ID)
}

func displayChat(chat tgapp.Chat) string {
	title := safeHuman(chat.Title)
	if chat.Username != "" {
		return title + " @" + safeHuman(chat.Username)
	}
	return title
}

func safeHuman(value string) string {
	return output.SanitizeTerminal(value)
}

func indentHumanText(value string) string {
	return strings.ReplaceAll(strings.TrimSpace(safeHuman(value)), "\n", "\n    ")
}
