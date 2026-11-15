package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/rm-hull/cf-tunnel-sync/internal"
	"github.com/rm-hull/cf-tunnel-sync/internal/config"
	"github.com/rm-hull/cf-tunnel-sync/internal/docker"
	"github.com/rm-hull/cf-tunnel-sync/internal/labels"
	"github.com/rm-hull/cf-tunnel-sync/internal/logx"
	"github.com/spf13/cobra"
)

func NewRulesCmd() *cobra.Command {
	jsonOut := false
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "Print desired rules from live Docker",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cmd.Flag("config").Value.String())
			if err != nil {
				return err
			}

			logLevel := logx.ParseLogLevel(cfg.Server.LogLevel)
			log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
				Level:       logLevel,
				AddSource:   true,
				ReplaceAttr: logx.ReplaceAttr,
			}))

			api, err := docker.New(cfg.Docker.Host, log)
			if err != nil {
				return err
			}

			rules, issues, err := api.DesiredRules(cmd.Context(), cfg.Labels.Prefixes, cfg.Labels.AllowUnprotectedAccess)
			if err != nil {
				return err
			}

			if jsonOut {
				return printRulesJSON(cmd.OutOrStdout(), rules, issues)
			}
			return printRulesTable(cmd.OutOrStdout(), rules, issues)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output as JSON")
	return cmd
}

func printRulesTable(w io.Writer, rules map[string]labels.Rule, issues []labels.Issue) error {
	keys := internal.SortKeys(rules)

	tbl := table.NewWriter()
	tbl.SetOutputMirror(w)
	tbl.SetStyle(table.StyleRounded)

	tbl.AppendHeader(table.Row{"HOST", "PATH", "SERVICE", "CONTAINER", "SOURCE"})
	for _, key := range keys {
		r := rules[key]
		tbl.AppendRow(table.Row{r.Hostname, r.Path, r.Service, r.ContainerName, r.SourcePrefix})
	}
	if len(keys) > 0 {
		tbl.Render()
	}

	if len(issues) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "ISSUES:")
		for _, iss := range issues {
			fmt.Fprintf(w, "  %s (%s): %s\n", iss.Kind, iss.Container, iss.Detail)
		}
	}
	return nil
}

func printRulesJSON(w io.Writer, rules map[string]labels.Rule, issues []labels.Issue) error {
	type ruleEntry struct {
		Key  string      `json:"key"`
		Rule labels.Rule `json:"rule"`
	}
	type issueEntry struct {
		Kind      string `json:"kind"`
		Container string `json:"container,omitempty"`
		Field     string `json:"field,omitempty"`
		Detail    string `json:"detail"`
	}
	type output struct {
		Rules  []ruleEntry  `json:"rules"`
		Issues []issueEntry `json:"issues,omitempty"`
	}

	keys := internal.SortKeys(rules)

	out := output{}
	for _, key := range keys {
		out.Rules = append(out.Rules, ruleEntry{
			Key:  key,
			Rule: rules[key],
		})
	}
	for _, iss := range issues {
		out.Issues = append(out.Issues, issueEntry{
			Kind:      iss.Kind,
			Container: iss.Container,
			Field:     iss.Field,
			Detail:    iss.Detail,
		})
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(w, string(data))
	return nil
}
