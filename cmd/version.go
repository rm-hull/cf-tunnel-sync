package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func NewVersionCmd(version, commit string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(c *cobra.Command, _ []string) error {
			fmt.Fprintf(c.OutOrStdout(), "cf-tunnel-sync %s (commit: %s)\n", version, commit)
			return nil
		},
	}
}
