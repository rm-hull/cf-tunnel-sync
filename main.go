package main

import (
	"os"

	"github.com/rm-hull/cf-tunnel-sync/cmd"
	"github.com/spf13/cobra"
)

// Version and Commit are set by ldflags at build time.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "cf-tunnel-sync",
		Short: "Keep Cloudflare Tunnel ingress rules in sync with Docker labels",
	}

	cfgPath := c.PersistentFlags().String("config", "", "path to config file")

	c.AddCommand(cmd.NewVersionCmd(version, commit))
	c.AddCommand(cmd.NewRunCmd())
	c.AddCommand(cmd.NewReconcileCmd())
	c.AddCommand(cmd.NewPlanCmd())
	c.AddCommand(cmd.NewRulesCmd())
	c.AddCommand(cmd.NewConfigCmd(cfgPath))
	c.AddCommand(cmd.NewHealthcheckCmd())

	return c
}
