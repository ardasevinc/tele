package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/tele/internal/output"
	tgapp "github.com/ardasevinc/tele/internal/telegram"
)

func chatsCommand(s *appState) *cobra.Command {
	var limit int
	var cursor string
	cmd := &cobra.Command{
		Use:     "chats",
		Aliases: []string{"dialogs"},
		Short:   "List accessible Telegram chats",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			limit = s.defaultLimit(limit)
			page, err := app.Chats(cmd.Context(), tgapp.ChatOptions{Limit: limit, Cursor: cursor})
			if err != nil {
				return err
			}
			meta := s.telegramMeta(cmd.Context(), app, limit, "", nil)
			applyRetrievalReceipt(&meta, page.Receipt)
			return writeValueWithMeta(s, page.Items, meta, func(w output.Writer) error {
				if _, err := fmt.Fprintln(w.Out, retrievalSummary(meta)); err != nil {
					return err
				}
				for _, chat := range page.Items {
					if _, err := fmt.Fprintf(w.Out, "%-22s %-10s %4d %s\n", safeHuman(chat.Ref), safeHuman(chat.Kind), chat.UnreadCount, displayChat(chat)); err != nil {
						return err
					}
				}
				return nil
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum chats to return")
	cmd.Flags().StringVar(&cursor, "cursor", "", "opaque cursor returned by a previous chats call")
	return cmd
}

func readCommand(s *appState) *cobra.Command {
	var limit int
	var format string
	var since string
	var until string
	var afterID int
	var beforeID int
	var aroundID int
	var chronological bool
	var cursor string
	cmd := &cobra.Command{
		Use:     "read <peer>",
		Aliases: []string{"history"},
		Short:   "Read bounded message history from a peer",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "human" && format != "transcript" {
				return fmt.Errorf("--format must be human or transcript")
			}
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			limit = s.defaultLimit(limit)
			opts := tgapp.ReadOptions{
				Peer:          args[0],
				Limit:         limit,
				AfterID:       afterID,
				BeforeID:      beforeID,
				AroundID:      aroundID,
				Chronological: chronological,
			}
			if since != "" {
				opts.Since, err = parseTimeFilter(since, time.Now())
				if err != nil {
					return err
				}
			}
			if until != "" {
				opts.Until, err = parseTimeFilter(until, time.Now())
				if err != nil {
					return err
				}
			}
			if format == "transcript" && !s.json && !s.jsonl {
				opts.Chronological = true
			}
			opts.Cursor = cursor
			page, err := app.Read(cmd.Context(), opts)
			if err != nil {
				return err
			}
			meta := s.telegramMeta(cmd.Context(), app, limit, args[0], nil)
			applyRetrievalReceipt(&meta, page.Receipt)
			if format == "transcript" && !s.json && !s.jsonl {
				return writeTranscript(s, page.Items, meta, app.PeerInfo(args[0]))
			}
			return writeMessages(s, page.Items, meta)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum messages to return")
	cmd.Flags().StringVar(&format, "format", "human", "output format: human or transcript")
	cmd.Flags().StringVar(&since, "since", "", "only include messages since duration/date/RFC3339")
	cmd.Flags().StringVar(&until, "until", "", "only include messages until duration/date/RFC3339")
	cmd.Flags().IntVar(&afterID, "after-id", 0, "only include messages after this id")
	cmd.Flags().IntVar(&beforeID, "before-id", 0, "only include messages before this id")
	cmd.Flags().IntVar(&aroundID, "around", 0, "fetch context around this message id")
	cmd.Flags().BoolVar(&chronological, "chronological", false, "print oldest messages first")
	cmd.Flags().StringVar(&cursor, "cursor", "", "opaque cursor returned by a previous read")
	return cmd
}

func searchCommand(s *appState) *cobra.Command {
	var limit int
	var chat string
	var cursor string
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search Telegram messages conservatively",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			limit = s.defaultLimit(limit)
			page, err := app.Search(cmd.Context(), tgapp.SearchOptions{Query: args[0], Peer: chat, Limit: limit, Cursor: cursor})
			if err != nil {
				return err
			}
			meta := s.telegramMeta(cmd.Context(), app, limit, chat, nil)
			applyRetrievalReceipt(&meta, page.Receipt)
			return writeMessages(s, page.Items, meta)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum messages to return")
	cmd.Flags().StringVar(&chat, "chat", "", "scope search to peer ref, username, or cached title")
	cmd.Flags().StringVar(&cursor, "cursor", "", "opaque cursor returned by a previous search")
	return cmd
}

func exportCommand(s *appState) *cobra.Command {
	var limit int
	var format string
	var cursor string
	var since string
	var until string
	cmd := &cobra.Command{
		Use:   "export <peer>",
		Short: "Bounded export of recent messages",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "jsonl" && format != "markdown" && format != "transcript" {
				return fmt.Errorf("--format must be jsonl, markdown, or transcript")
			}
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			limit = s.defaultLimit(limit)
			opts := tgapp.ReadOptions{Peer: args[0], Limit: limit, Chronological: format == "transcript", Cursor: cursor}
			if since != "" {
				opts.Since, err = parseTimeFilter(since, time.Now())
				if err != nil {
					return err
				}
			}
			if until != "" {
				opts.Until, err = parseTimeFilter(until, time.Now())
				if err != nil {
					return err
				}
			}
			page, err := app.Read(cmd.Context(), opts)
			if err != nil {
				return err
			}
			meta := s.telegramMeta(cmd.Context(), app, limit, args[0], nil)
			applyRetrievalReceipt(&meta, page.Receipt)
			if s.json || s.jsonl {
				return writeMessages(s, page.Items, meta)
			}
			if format == "jsonl" {
				return writeMessagesWithFormat(s, page.Items, meta, output.JSONL)
			}
			if format == "transcript" {
				return writeTranscript(s, page.Items, meta, app.PeerInfo(args[0]))
			}
			if _, err := fmt.Fprintln(s.out, retrievalSummary(meta)); err != nil {
				return err
			}
			for _, msg := range page.Items {
				if _, err := fmt.Fprintf(s.out, "- %s #%d %s\n", safeHuman(msg.Date), msg.ID, indentHumanText(msg.Text)); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum messages to export")
	cmd.Flags().StringVar(&format, "format", "jsonl", "export format: jsonl, markdown, or transcript")
	cmd.Flags().StringVar(&cursor, "cursor", "", "opaque cursor returned by a previous export")
	cmd.Flags().StringVar(&since, "since", "", "only include messages since duration/date/RFC3339")
	cmd.Flags().StringVar(&until, "until", "", "only include messages until duration/date/RFC3339")
	return cmd
}

func inboxCommand(s *appState) *cobra.Command {
	return inboxLikeCommand(s, "inbox", "", "List recent dialogs for triage")
}

func inboxLikeCommand(s *appState, name, mode, short string) *cobra.Command {
	var limit int
	var cursor string
	cmd := &cobra.Command{
		Use:   name,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			limit = s.defaultLimit(limit)
			page, err := app.Inbox(cmd.Context(), tgapp.ChatOptions{Limit: limit, Cursor: cursor}, mode)
			if err != nil {
				return err
			}
			meta := s.telegramMeta(cmd.Context(), app, limit, "", nil)
			applyRetrievalReceipt(&meta, page.Receipt)
			return writeValueWithMeta(s, page.Items, meta, func(w output.Writer) error {
				if _, err := fmt.Fprintln(w.Out, retrievalSummary(meta)); err != nil {
					return err
				}
				for _, chat := range page.Items {
					if _, err := fmt.Fprintf(w.Out, "%-22s unread=%-3d mentions=%-3d #%d %s %s\n", safeHuman(chat.Ref), chat.UnreadCount, chat.UnreadMentionsCount, chat.TopMessageID, displayChat(chat), indentHumanText(chat.LastMessagePreview)); err != nil {
						return err
					}
				}
				return nil
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum dialogs to return")
	cmd.Flags().StringVar(&cursor, "cursor", "", "opaque cursor returned by a previous dialog listing")
	if name == "inbox" {
		cmd.AddCommand(inboxLikeCommand(s, "unread", "unread", "List dialogs with unread messages"))
		cmd.AddCommand(inboxLikeCommand(s, "mentions", "mentions", "List dialogs with unread mentions"))
	}
	return cmd
}

func mediaCommand(s *appState) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "media",
		Short: "Inspect and download message media",
	}
	cmd.AddCommand(mediaDownloadCommand(s))
	return cmd
}

func mediaDownloadCommand(s *appState) *cobra.Command {
	var outDir string
	cmd := &cobra.Command{
		Use:   "download <peer> <msg-id>",
		Short: "Download media from one message",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			msgID, err := parsePositiveInt(args[1], "msg-id")
			if err != nil {
				return err
			}
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			result, err := app.DownloadMedia(cmd.Context(), tgapp.MediaDownloadOptions{
				Peer:      args[0],
				MessageID: msgID,
				OutDir:    outDir,
			})
			if err != nil {
				return err
			}
			return writeValueWithMeta(s, result, s.telegramMeta(cmd.Context(), app, 0, result.PeerRef, nil), func(w output.Writer) error {
				return w.Print(fmt.Sprintf("downloaded %s", safeHuman(result.Path)))
			})
		},
	}
	cmd.Flags().StringVar(&outDir, "out-dir", "", "directory for downloaded media; defaults to a new temp directory")
	return cmd
}
