package cli_test

import (
	"bytes"
	"testing"

	"github.com/rm-hull/cf-tunnel-sync/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionCommand(t *testing.T) {
	cmd := cli.NewRootCmd(&cli.Deps{})
	cmd.SetArgs([]string{"version"})

	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	require.NoError(t, cmd.Execute())

	output := out.String()
	assert.Contains(t, output, "cf-tunnel-sync")
	assert.Contains(t, output, "commit:")
}

func TestUnknownSubcommand(t *testing.T) {
	cmd := cli.NewRootCmd(&cli.Deps{})
	cmd.SetArgs([]string{"nonexistent"})

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	require.Error(t, err, "expected error for unknown subcommand")
}

func TestHelpListsSubcommands(t *testing.T) {
	cmd := cli.NewRootCmd(&cli.Deps{})
	cmd.SetArgs([]string{"--help"})

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	// Help command returns a special error in cobra
	_ = cmd.Execute()

	out := buf.String()
	expectedCommands := []string{"run", "reconcile", "plan", "rules", "config", "healthcheck"}
	for _, sub := range expectedCommands {
		assert.Contains(t, out, sub, "help output should contain subcommand '%s'", sub)
	}
}

func TestConfigSubcommands(t *testing.T) {
	cmd := cli.NewRootCmd(&cli.Deps{})

	// Check that config subcommands exist
	configCmd, _, err := cmd.Find([]string{"config"})
	require.NoError(t, err, "config command not found")

	expectedSubs := []string{"print", "validate", "schema"}
	for _, sub := range expectedSubs {
		_, _, err := configCmd.Find([]string{sub})
		assert.NoError(t, err, "config subcommand '%s' not found", sub)
	}
}
