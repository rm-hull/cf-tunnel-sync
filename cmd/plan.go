package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/rm-hull/cf-tunnel-sync/internal/cf"
	"github.com/rm-hull/cf-tunnel-sync/internal/config"
	"github.com/rm-hull/cf-tunnel-sync/internal/docker"
	"github.com/rm-hull/cf-tunnel-sync/internal/labels"
	"github.com/rm-hull/cf-tunnel-sync/internal/logx"
	rulespkg "github.com/rm-hull/cf-tunnel-sync/internal/rules"
	"github.com/spf13/cobra"
)

func NewPlanCmd() *cobra.Command {
	jsonOut := false
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Dry-run reconciliation and print the diff",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cmd.Flag("config").Value.String())
			if err != nil {
				return err
			}

			logLevel := logx.ParseLogLevel(cfg.Server.LogLevel)
			log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
				Level:     logLevel,
				AddSource: true,
			}))

			dockerAPI, err := docker.New(cfg.Docker.Host, log)
			if err != nil {
				return err
			}

			cfAPI, err := cf.New(
				string(cfg.Cloudflare.APIToken),
				cfg.Cloudflare.AccountID,
				cfg.Cloudflare.ZoneID,
				log,
			)
			if err != nil {
				return err
			}

			desiredRules, _, err := dockerAPI.DesiredRules(cmd.Context(), cfg.Labels.Prefixes, cfg.Labels.AllowUnprotectedAccess)
			if err != nil {
				return err
			}

			// Convert labels.Rule to rules.Rule
			desired := parsePlanRules(desiredRules)

			tunnel, err := cfAPI.FindTunnel(cmd.Context(), cfg.Tunnel.Name)
			if err != nil {
				return err
			}

			result, err := cfAPI.Plan(cmd.Context(), tunnel.ID, desired, rulespkg.NewState(), cfg.DNS.OverwriteConflicts)
			if err != nil {
				return err
			}

			if jsonOut {
				return printPlanJSON(cmd.OutOrStdout(), result)
			}
			return printPlanTable(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output as JSON")
	return cmd
}

func printPlanTable(w io.Writer, result cf.PlanResult) error {
	fmt.Fprintf(w, "Tunnel: %s (%s)\n", result.Tunnel.Name, result.Tunnel.ID)
	fmt.Fprintln(w)

	if len(result.IngressActions) > 0 {
		fmt.Fprintln(w, "INGRESS CHANGES:")
		for _, a := range result.IngressActions {
			switch a.Kind {
			case "add":
				fmt.Fprintf(w, "  + %s\n", a.Detail)
			case "remove":
				fmt.Fprintf(w, "  - %s (%s)\n", a.RuleKey, a.Detail)
			}
		}
		fmt.Fprintln(w)
	}

	if len(result.DNSActions) > 0 {
		fmt.Fprintln(w, "DNS CHANGES:")
		for _, a := range result.DNSActions {
			switch a.Kind {
			case "dns_create":
				fmt.Fprintf(w, "  + %s\n", a.Detail)
			case "dns_adopt":
				fmt.Fprintf(w, "  ~ %s (adopt)\n", a.Detail)
			case "dns_conflict":
				fmt.Fprintf(w, "  ! %s\n", a.Detail)
			case "dns_no_zone":
				fmt.Fprintf(w, "  ? %s\n", a.Detail)
			}
		}
		fmt.Fprintln(w)
	}

	if len(result.OrphanDNS) > 0 {
		fmt.Fprintln(w, "ORPHAN DNS:")
		for _, r := range result.OrphanDNS {
			fmt.Fprintf(w, "  %s -> %s (zone: %s)\n", r.Name, r.Content, r.ZoneName)
		}
		fmt.Fprintln(w)
	}

	if len(result.IngressActions) == 0 && len(result.DNSActions) == 0 && len(result.OrphanDNS) == 0 {
		fmt.Fprintln(w, "No changes needed.")
	}

	return nil
}

func printPlanJSON(w io.Writer, result cf.PlanResult) error {
	type output struct {
		Tunnel         cf.Tunnel       `json:"tunnel"`
		IngressActions []cf.PlanAction `json:"ingress_actions"`
		DNSActions     []cf.PlanAction `json:"dns_actions"`
		OrphanDNS      []cf.DNSRecord  `json:"orphan_dns"`
	}

	out := output{
		Tunnel:         result.Tunnel,
		IngressActions: result.IngressActions,
		DNSActions:     result.DNSActions,
		OrphanDNS:      result.OrphanDNS,
	}

	if out.IngressActions == nil {
		out.IngressActions = []cf.PlanAction{}
	}
	if out.DNSActions == nil {
		out.DNSActions = []cf.PlanAction{}
	}
	if out.OrphanDNS == nil {
		out.OrphanDNS = []cf.DNSRecord{}
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(w, string(data))
	return nil
}

// parsePlanRules converts desired labels rules to rulespkg rules for the plan.
func parsePlanRules(desiredRules map[string]labels.Rule) map[string]rulespkg.Rule {
	desired := make(map[string]rulespkg.Rule)
	for key, r := range desiredRules {
		desired[key] = rulespkg.Rule{
			Hostname:         r.Hostname,
			Path:             r.Path,
			Service:          r.Service,
			NoTLSVerify:      r.NoTLSVerify,
			OriginServerName: r.OriginServerName,
			ZoneName:         r.ZoneName,
			ContainerName:    r.ContainerName,
			SourcePrefix:     r.SourcePrefix,
			SourceIndex:      r.SourceIndex,
			Status:           "active",
		}
	}
	return desired
}
