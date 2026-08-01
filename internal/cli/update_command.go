package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/tele/internal/buildinfo"
	"github.com/ardasevinc/tele/internal/output"
	"github.com/ardasevinc/tele/internal/updater"
)

func updateCommand(s *appState) *cobra.Command {
	var check, yes bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check for or explicitly apply a Tele update",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if check == yes {
				return fmt.Errorf("exactly one of --check or --yes is required")
			}
			if yes && s.dryRun {
				return fmt.Errorf("--dry-run is not supported for update; use update --check")
			}
			client := s.updateClient
			if client == nil {
				client = updater.NewClient()
			}
			result, err := client.Check(cmd.Context(), buildinfo.Version, buildinfo.Commit)
			if err != nil {
				return err
			}
			if yes {
				result, err = client.Apply(cmd.Context(), result)
				if err != nil {
					return err
				}
			}
			return writeValue(s, result, func(w output.Writer) error {
				return writeUpdateHuman(w, result)
			})
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "check the latest immutable stable release without changing anything")
	cmd.Flags().BoolVar(&yes, "yes", false, "explicitly apply the supported pinned update")
	return cmd
}

func writeUpdateHuman(w output.Writer, result updater.Result) error {
	if _, err := fmt.Fprintf(w.Out, "tele %s (%s)\nlatest: %s\nstatus: %s\ninstall: %s\nexecutable: %s\nresolved: %s\n",
		safeHuman(result.CurrentVersion), safeHuman(result.CurrentCommit), safeHuman(result.LatestVersion),
		safeHuman(string(result.Status)), safeHuman(result.InstallManager), safeHuman(result.ExecutablePath), safeHuman(result.ResolvedPath)); err != nil {
		return err
	}
	if result.Applied {
		_, err := fmt.Fprintf(w.Out, "updated: %s (verified)\n", safeHuman(result.VerifiedVersion))
		return err
	}
	if result.UnsupportedReason != "" {
		if _, err := fmt.Fprintf(w.Out, "automatic update: unavailable (%s)\n", safeHuman(result.UnsupportedReason)); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w.Out, "command: %s\n", safeHuman(result.RecommendedCommand))
	return err
}
