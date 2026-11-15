package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/rm-hull/cf-tunnel-sync/internal/cli"
)

func TestVersionCommand(t *testing.T) {
	cmd := cli.NewRootCmd(&cli.Deps{})
	cmd.SetArgs([]string{"version"})

	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("version command failed: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "cf-tunnel-sync") {
		t.Errorf("version output missing 'cf-tunnel-sync': %s", output)
	}
	if !strings.Contains(output, "commit:") {
		t.Errorf("version output missing 'commit:': %s", output)
	}
}

func TestUnknownSubcommand(t *testing.T) {
	cmd := cli.NewRootCmd(&cli.Deps{})
	cmd.SetArgs([]string{"nonexistent"})

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
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
		if !strings.Contains(out, sub) {
			t.Errorf("help output missing subcommand '%s': %s", sub, out)
		}
	}
}

func TestConfigSubcommands(t *testing.T) {
	cmd := cli.NewRootCmd(&cli.Deps{})

	// Check that config subcommands exist
	configCmd, _, err := cmd.Find([]string{"config"})
	if err != nil {
		t.Fatalf("config command not found: %v", err)
	}

	expectedSubs := []string{"print", "validate", "schema"}
	for _, sub := range expectedSubs {
		_, _, err := configCmd.Find([]string{sub})
		if err != nil {
			t.Errorf("config subcommand '%s' not found: %v", sub, err)
		}
	}
}
