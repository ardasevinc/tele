package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	tgapp "github.com/ardasevinc/tele/internal/telegram"
)

func mutationCommands(s *appState) []*cobra.Command {
	return []*cobra.Command{
		sendCommand(s),
		replyCommand(s),
		reactCommand(s),
		editCommand(s),
		deleteCommand(s),
		inboxLikeCommand(s, "unread", "unread", "List dialogs with unread messages"),
		inboxLikeCommand(s, "mentions", "mentions", "List dialogs with unread mentions"),
	}
}

func sendCommand(s *appState) *cobra.Command {
	var text string
	var textStdin bool
	cmd := &cobra.Command{
		Use:   "send <peer>",
		Short: "Send a text message",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := s.requireWritable("send"); err != nil {
				return err
			}
			if err := validateTextSources(text, textStdin); err != nil {
				return err
			}
			if s.dryRun {
				return previewMutation(s, cmd.Context(), "send", args[0], 0, "")
			}
			body, err := textInput(s, text, textStdin)
			if err != nil {
				return err
			}
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			result, err := app.Send(cmd.Context(), args[0], body, 0)
			if err != nil {
				return err
			}
			return writeMutationResult(s, result, s.telegramMeta(cmd.Context(), app, 0, result.PeerRef, nil), s.mutationReceipt(fmt.Sprintf("sent %s #%d", result.PeerRef, result.MessageID)))
		},
	}
	cmd.Flags().StringVar(&text, "text", "", "message text")
	cmd.Flags().BoolVar(&textStdin, "text-stdin", false, "read message text from stdin")
	return cmd
}

func replyCommand(s *appState) *cobra.Command {
	var text string
	var textStdin bool
	cmd := &cobra.Command{
		Use:   "reply <peer> <msg-id>",
		Short: "Reply to a message",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := s.requireWritable("reply"); err != nil {
				return err
			}
			if err := validateTextSources(text, textStdin); err != nil {
				return err
			}
			msgID, err := parsePositiveInt(args[1], "msg-id")
			if err != nil {
				return err
			}
			if s.dryRun {
				return previewMutation(s, cmd.Context(), "reply", args[0], msgID, "")
			}
			body, err := textInput(s, text, textStdin)
			if err != nil {
				return err
			}
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			result, err := app.Send(cmd.Context(), args[0], body, msgID)
			if err != nil {
				return err
			}
			result.Action = "reply"
			return writeMutationResult(s, result, s.telegramMeta(cmd.Context(), app, 0, result.PeerRef, nil), s.mutationReceipt(fmt.Sprintf("replied %s #%d", result.PeerRef, result.MessageID)))
		},
	}
	cmd.Flags().StringVar(&text, "text", "", "message text")
	cmd.Flags().BoolVar(&textStdin, "text-stdin", false, "read message text from stdin")
	return cmd
}

func reactCommand(s *appState) *cobra.Command {
	var emoji string
	cmd := &cobra.Command{
		Use:   "react <peer> <msg-id>",
		Short: "React to a message",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := s.requireWritable("react"); err != nil {
				return err
			}
			msgID, err := parsePositiveInt(args[1], "msg-id")
			if err != nil {
				return err
			}
			if strings.TrimSpace(emoji) == "" {
				return fmt.Errorf("--emoji is required")
			}
			if s.dryRun {
				return previewMutation(s, cmd.Context(), "react", args[0], msgID, "")
			}
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			result, err := app.React(cmd.Context(), args[0], msgID, emoji)
			if err != nil {
				return err
			}
			return writeMutationResult(s, result, s.telegramMeta(cmd.Context(), app, 0, result.PeerRef, nil), s.mutationReceipt(fmt.Sprintf("reacted %s #%d", result.PeerRef, result.MessageID)))
		},
	}
	cmd.Flags().StringVar(&emoji, "emoji", "", "reaction emoji")
	return cmd
}

func editCommand(s *appState) *cobra.Command {
	var text string
	var textStdin bool
	cmd := &cobra.Command{
		Use:   "edit <peer> <msg-id>",
		Short: "Edit one of your messages",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := s.requireWritable("edit"); err != nil {
				return err
			}
			if err := validateTextSources(text, textStdin); err != nil {
				return err
			}
			msgID, err := parsePositiveInt(args[1], "msg-id")
			if err != nil {
				return err
			}
			if s.dryRun {
				return previewMutation(s, cmd.Context(), "edit", args[0], msgID, "")
			}
			body, err := textInput(s, text, textStdin)
			if err != nil {
				return err
			}
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			result, err := app.Edit(cmd.Context(), args[0], msgID, body)
			if err != nil {
				return err
			}
			return writeMutationResult(s, result, s.telegramMeta(cmd.Context(), app, 0, result.PeerRef, nil), s.mutationReceipt(fmt.Sprintf("edited %s #%d", result.PeerRef, result.MessageID)))
		},
	}
	cmd.Flags().StringVar(&text, "text", "", "new message text")
	cmd.Flags().BoolVar(&textStdin, "text-stdin", false, "read new message text from stdin")
	return cmd
}

func deleteCommand(s *appState) *cobra.Command {
	var forMe, revoke, yes bool
	cmd := &cobra.Command{
		Use:   "delete <peer> <msg-id>",
		Short: "Delete a message",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := s.requireWritable("delete"); err != nil {
				return err
			}
			if !yes && !s.dryRun {
				return fmt.Errorf("delete requires --yes")
			}
			if forMe == revoke {
				return fmt.Errorf("choose exactly one of --for-me or --revoke")
			}
			msgID, err := parsePositiveInt(args[1], "msg-id")
			if err != nil {
				return err
			}
			scope := tgapp.DeleteScopeForMe
			if revoke {
				scope = tgapp.DeleteScopeRevoke
			}
			if s.dryRun {
				return previewMutation(s, cmd.Context(), "delete", args[0], msgID, scope)
			}
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			result, err := app.DeleteMessage(cmd.Context(), args[0], msgID, scope)
			if err != nil {
				return err
			}
			return writeMutationResult(s, result, s.telegramMeta(cmd.Context(), app, 0, result.PeerRef, nil), s.mutationReceipt(fmt.Sprintf("deleted %s #%d", result.PeerRef, result.MessageID)))
		},
	}
	cmd.Flags().BoolVar(&forMe, "for-me", false, "delete only for the current account where Telegram supports it")
	cmd.Flags().BoolVar(&revoke, "revoke", false, "delete for everyone where Telegram supports it")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm deletion")
	return cmd
}
