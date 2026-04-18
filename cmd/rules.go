package cmd

import (
	"fmt"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/xdamman/agentdesk/internal/rules"
	"github.com/xdamman/agentdesk/internal/tui"
)

var (
	rulesJSON bool
)

func init() {
	rootCmd.AddCommand(rulesCmd)
	rulesCmd.AddCommand(rulesRmCmd)
	rulesCmd.Flags().BoolVar(&rulesJSON, "json", false, "Print as JSON (non-interactive)")
}

var rulesCmd = &cobra.Command{
	Use:   "rules",
	Short: "Auto-approve rules used by `agentdesk daemon`",
	Long: `List auto-approve rules.

Rules are created automatically when you run 'agentdesk requests approve <id>'
on an authorization whose 2-second Stripe window has already expired. The
daemon uses them to auto-approve the next matching webhook in time.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		rs, err := rules.Load()
		if err != nil {
			return err
		}
		if rulesJSON {
			return printJSON(rs.Rules)
		}
		if len(rs.Rules) == 0 {
			fmt.Println("No auto-approve rules yet.")
			return nil
		}
		cols := []table.Column{
			{Title: "ID", Width: 22},
			{Title: "AGENT", Width: 18},
			{Title: "MERCHANT", Width: 22},
			{Title: "AMOUNT", Width: 10},
			{Title: "DATE", Width: 12},
			{Title: "MATCHED", Width: 16},
		}
		var rows []table.Row
		for _, r := range rs.Rules {
			matched := "—"
			if r.LastMatched != nil {
				matched = r.LastMatched.Format("Jan 02 15:04")
			}
			rows = append(rows, table.Row{
				r.ID,
				dashIfEmpty(r.Agent),
				r.Merchant,
				fmt.Sprintf("€%.2f", float64(r.Amount)/100),
				r.Date,
				matched,
			})
		}
		if !interactive(rulesJSON) {
			printPlainTable(cols, rows)
			return nil
		}
		_, err = tea.NewProgram(tui.NewTable("Auto-approve rules", cols, rows)).Run()
		return err
	},
}

var rulesRmCmd = &cobra.Command{
	Use:   "rm [ruleId]",
	Short: "Remove an auto-approve rule by id",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		rs, err := rules.Load()
		if err != nil {
			return err
		}
		if !rs.RemoveByID(args[0]) {
			return fmt.Errorf("no rule with id %q", args[0])
		}
		if err := rs.Save(); err != nil {
			return err
		}
		fmt.Printf("✓ Removed rule %s\n", args[0])
		return nil
	},
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
