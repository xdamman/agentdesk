package cmd

import (
	"fmt"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	stripe "github.com/stripe/stripe-go/v82"

	"github.com/xdamman/agentdesk/internal/store"
	"github.com/xdamman/agentdesk/internal/stripeapi"
	"github.com/xdamman/agentdesk/internal/tui"
)

var cardsJSON bool

func init() {
	rootCmd.AddCommand(cardsCmd)
	cardsCmd.Flags().BoolVar(&cardsJSON, "json", false, "Print as JSON (non-interactive)")
}

var cardsCmd = &cobra.Command{
	Use:   "cards",
	Short: "Show the list of virtual cards",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireConfig(); err != nil {
			return err
		}
		cards, err := stripeapi.ListCards()
		if err != nil {
			return err
		}
		s, _ := store.Load()

		if cardsJSON {
			out := make([]map[string]any, 0, len(cards))
			for _, c := range cards {
				out = append(out, map[string]any{
					"card_id": c.ID,
					"agent":   agentNameForCard(s, c),
					"last4":   c.Last4,
					"brand":   string(c.Brand),
					"status":  string(c.Status),
					"limit":   formatCardLimit(c),
				})
			}
			return printJSON(out)
		}

		if len(cards) == 0 {
			fmt.Println("No cards yet. Create one with `agentdesk agents add`.")
			return nil
		}

		cols := []table.Column{
			{Title: "AGENT", Width: 22},
			{Title: "CARD ID", Width: 22},
			{Title: "LAST4", Width: 7},
			{Title: "BRAND", Width: 10},
			{Title: "STATUS", Width: 10},
			{Title: "LIMIT", Width: 22},
		}
		var rows []table.Row
		for _, c := range cards {
			rows = append(rows, table.Row{
				agentNameForCard(s, c), c.ID, c.Last4, string(c.Brand), string(c.Status),
				formatCardLimit(c),
			})
		}
		if !interactive(cardsJSON) {
			printPlainTable(cols, rows)
			return nil
		}
		_, err = tea.NewProgram(tui.NewTable("Virtual cards", cols, rows)).Run()
		return err
	},
}

func agentNameForCard(s *store.Store, c *stripe.IssuingCard) string {
	if a := s.FindByCard(c.ID); a != nil {
		return a.Name
	}
	if v, ok := c.Metadata["agentdesk_agent"]; ok && v != "" {
		return v
	}
	return "—"
}

func formatCardLimit(c *stripe.IssuingCard) string {
	if c.SpendingControls == nil || len(c.SpendingControls.SpendingLimits) == 0 {
		return "—"
	}
	lim := c.SpendingControls.SpendingLimits[0]
	return fmt.Sprintf("€%.2f / %s", float64(lim.Amount)/100, lim.Interval)
}
