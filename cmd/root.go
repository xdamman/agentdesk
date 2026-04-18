package cmd

import (
	"github.com/spf13/cobra"

	"github.com/xdamman/agentdesk/internal/config"
	"github.com/xdamman/agentdesk/internal/stripeapi"
)

var rootCmd = &cobra.Command{
	Use:           "agentdesk",
	Short:         "Spendesk for AI agents — virtual cards, policies, approvals",
	SilenceUsage:  true,
	SilenceErrors: true,
}

// SetVersion wires the binary's build-time version string into Cobra so
// `agentdesk --version` works.
func SetVersion(v string) {
	rootCmd.Version = v
}

func Execute() error {
	return rootCmd.Execute()
}

// requireConfig loads stripe config and initializes the API client.
// Called by every subcommand except `setup`.
func requireConfig() error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	stripeapi.Init(c.StripeAPIKey)
	return nil
}
