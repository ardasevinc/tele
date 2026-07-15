package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/tele/internal/buildinfo"
	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/doctor"
	"github.com/ardasevinc/tele/internal/output"
)

func doctorCommand(s *appState) *cobra.Command {
	var connect bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check local tele readiness",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := config.DefaultPaths()
			if err != nil {
				return err
			}
			if s.cfgPath != "" {
				paths.Config = s.cfgPath
			}
			backend, supported := s.secretBackendInfo()
			report := doctor.Run(cmd.Context(), doctor.Options{
				Paths:                  paths,
				Profile:                s.profile,
				Secrets:                s.secrets(),
				SecretBackend:          backend,
				SecretBackendSupported: supported,
				Version:                buildinfo.Version,
				Commit:                 buildinfo.Commit,
				Connect:                connect,
				Probe: func(ctx context.Context) (bool, error) {
					app, err := s.telegramApp()
					if err != nil {
						return false, err
					}
					status, err := app.Status(ctx)
					return status.Authorized, err
				},
			})
			if err := writeValue(s, report, func(w output.Writer) error {
				return writeDoctorHuman(w, report)
			}); err != nil {
				return err
			}
			if !report.OK {
				return reportedError{code: output.ExitGeneral, err: fmt.Errorf("doctor found failed checks")}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&connect, "connect", false, "perform bounded Telegram connectivity and authorization checks")
	return cmd
}

func writeDoctorHuman(w output.Writer, report doctor.Report) error {
	if _, err := fmt.Fprintf(w.Out, "tele %s (%s)\nprofile: %s\nconfig: %s\ndata: %s\n\n",
		safeHuman(report.Version), safeHuman(report.Commit), safeHuman(report.Profile),
		safeHuman(report.ConfigPath), safeHuman(report.DataPath)); err != nil {
		return err
	}
	for _, check := range report.Checks {
		if _, err := fmt.Fprintf(w.Out, "%-8s %-20s %s%s\n", safeHuman(string(check.Status)), safeHuman(check.Name), safeHuman(check.Message), doctorDetails(check.Details)); err != nil {
			return err
		}
	}
	return nil
}

func doctorDetails(details map[string]any) string {
	if len(details) == 0 {
		return ""
	}
	keys := make([]string, 0, len(details))
	for key := range details {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", safeHuman(key), safeHuman(fmt.Sprint(details[key]))))
	}
	return " (" + strings.Join(parts, ", ") + ")"
}
