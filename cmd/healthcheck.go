package cmd

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/rm-hull/cf-tunnel-sync/internal/config"
	"github.com/spf13/cobra"
)

func NewHealthcheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "healthcheck",
		Short: "Check if the status page is responding",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfgPath := cmd.Flag("config").Value.String()
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}

			listen := cfg.Server.Listen
			if !strings.HasPrefix(listen, ":") {
				return fmt.Errorf("server.listen must be a port (e.g., :8080), got: %s", listen)
			}
			url := fmt.Sprintf("http://localhost%s/healthz", listen)

			resp, err := http.Get(url) //nolint:noctx,gosec // healthcheck URL is from local config
			if err != nil {
				return fmt.Errorf("healthcheck failed: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("healthcheck returned status %d", resp.StatusCode)
			}
			return nil
		},
	}
}
