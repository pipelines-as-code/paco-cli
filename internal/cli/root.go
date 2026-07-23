package cli

import (
	"fmt"

	"github.com/pipelines-as-code/paco-cli/internal/diff"
	"github.com/spf13/cobra"
)

func Root(version, commit, date string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "paco",
		Short: "Paco AI code reviewer CLI",
	}

	cmd.AddCommand(versionCmd(version, commit, date))
	cmd.AddCommand(diff.Command())

	return cmd
}

func versionCmd(version, commit, date string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Printf("paco %s (commit: %s, built: %s)\n", version, commit, date)
		},
	}
}
