package main_test

import (
	"bytes"
	"testing"

	"github.com/rm-hull/cf-tunnel-sync/cmd"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestRoot() *cobra.Command {
	c := &cobra.Command{Use: "cf-tunnel-sync"}
	cfgPath := c.PersistentFlags().String("config", "", "path to config file")
	c.AddCommand(cmd.NewVersionCmd("1.0.0", "abc123"))
	c.AddCommand(cmd.NewRunCmd())
	c.AddCommand(cmd.NewReconcileCmd())
	c.AddCommand(cmd.NewPlanCmd())
	c.AddCommand(cmd.NewRulesCmd())
	c.AddCommand(cmd.NewConfigCmd(cfgPath))
	c.AddCommand(cmd.NewHealthcheckCmd())
	return c
}

func TestVersionCommand(t *testing.T) {
	c := newTestRoot()
	c.SetArgs([]string{"version"})

	var out bytes.Buffer
	var errOut bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errOut)

	require.NoError(t, c.Execute())

	output := out.String()
	assert.Contains(t, output, "cf-tunnel-sync")
	assert.Contains(t, output, "commit:")
}

func TestUnknownSubcommand(t *testing.T) {
	c := newTestRoot()
	c.SetArgs([]string{"nonexistent"})

	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)

	err := c.Execute()
	require.Error(t, err, "expected error for unknown subcommand")
}

func TestHelpListsSubcommands(t *testing.T) {
	c := newTestRoot()
	c.SetArgs([]string{"--help"})

	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)

	// Help command returns a special error in cobra
	_ = c.Execute()

	out := buf.String()
	expectedCommands := []string{"run", "reconcile", "plan", "rules", "config", "healthcheck"}
	for _, sub := range expectedCommands {
		assert.Contains(t, out, sub, "help output should contain subcommand '%s'", sub)
	}
}

func TestConfigSubcommands(t *testing.T) {
	c := newTestRoot()

	// Check that config subcommands exist
	configCmd, _, err := c.Find([]string{"config"})
	require.NoError(t, err, "config command not found")

	expectedSubs := []string{"print", "validate", "schema"}
	for _, sub := range expectedSubs {
		_, _, err := configCmd.Find([]string{sub})
		assert.NoError(t, err, "config subcommand '%s' not found", sub)
	}
}
