package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	stripe "github.com/stripe/stripe-go/v82"

	"github.com/xdamman/agentdesk/internal/config"
	"github.com/xdamman/agentdesk/internal/store"
	"github.com/xdamman/agentdesk/internal/stripeapi"
	"github.com/xdamman/agentdesk/internal/tui"
)

var validIntervals = map[string]bool{"daily": true, "weekly": true, "monthly": true}

func init() {
	rootCmd.AddCommand(agentsCmd)
	agentsCmd.AddCommand(agentsAddCmd, agentsRmCmd, agentsEditCmd)

	agentsCmd.Flags().BoolVar(&agentsJSON, "json", false, "Print as JSON (non-interactive)")

	agentsAddCmd.Flags().StringVar(&addFlags.Name, "name", "", "Agent name (non-interactive)")
	agentsAddCmd.Flags().StringVar(&addFlags.Allowance, "allowance", "", "Allowance amount in EUR (e.g. 100.00)")
	agentsAddCmd.Flags().StringVar(&addFlags.Interval, "interval", "monthly", "Allowance interval: daily|weekly|monthly")
	agentsAddCmd.Flags().StringVar(&addFlags.Merchants, "merchants", "", "Allowed merchants, comma-separated (optional)")
	agentsAddCmd.Flags().BoolVar(&addFlags.JSON, "json", false, "Print result as JSON (implies non-interactive output)")

	agentsEditCmd.Flags().StringVar(&editFlags.Allowance, "allowance", "", "New allowance in EUR (non-interactive)")
	agentsEditCmd.Flags().StringVar(&editFlags.Interval, "interval", "", "New interval: daily|weekly|monthly (non-interactive)")
	agentsEditCmd.Flags().StringVar(&editFlags.Merchants, "merchants", "", "Allowed merchants, comma-separated. Use empty string to clear (non-interactive)")
}

var (
	agentsJSON bool
	editFlags  struct {
		Allowance string
		Interval  string
		Merchants string
	}
)

var addFlags struct {
	Name      string
	Allowance string
	Interval  string
	Merchants string
	JSON      bool
}

var agentsCmd = &cobra.Command{
	Use:   "agents",
	Short: "List agents and their policy + card balance",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireConfig(); err != nil {
			return err
		}
		s, err := store.Load()
		if err != nil {
			return err
		}

		type row struct {
			Name      string           `json:"name"`
			Last4     string           `json:"last4"`
			CardID    string           `json:"card_id"`
			Allowance store.Allowance  `json:"allowance"`
			Merchants []string         `json:"allowed_merchants,omitempty"`
			Spent     int64            `json:"spent_cents"`
			Remaining int64            `json:"remaining_cents"`
		}
		data := make([]row, 0, len(s.Agents))
		for _, a := range s.Agents {
			spent, _ := stripeapi.SpentThisPeriod(a.CardID, a.Allowance.Interval)
			data = append(data, row{
				Name: a.Name, Last4: a.Last4, CardID: a.CardID,
				Allowance: a.Allowance, Merchants: a.AllowedMerchants,
				Spent: spent, Remaining: a.Allowance.Amount - spent,
			})
		}

		if agentsJSON {
			return printJSON(data)
		}
		if len(data) == 0 {
			fmt.Println("No agents yet. Create one with `agentdesk agents add`.")
			return nil
		}

		cols := []table.Column{
			{Title: "NAME", Width: 22},
			{Title: "LAST4", Width: 7},
			{Title: "POLICY", Width: 22},
			{Title: "SPENT", Width: 14},
			{Title: "REMAINING", Width: 14},
		}
		rows := make([]table.Row, 0, len(data))
		for _, d := range data {
			rows = append(rows, table.Row{
				d.Name, d.Last4, store.FormatAllowance(d.Allowance),
				fmt.Sprintf("€%.2f", float64(d.Spent)/100),
				fmt.Sprintf("€%.2f", float64(d.Remaining)/100),
			})
		}
		if !interactive(agentsJSON) {
			printPlainTable(cols, rows)
			return nil
		}
		_, err = tea.NewProgram(tui.NewTable("Agents", cols, rows)).Run()
		return err
	},
}

var agentsAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Create a new agent and issue a virtual card",
	Long: `Create a new agent and issue a virtual card.

Runs an interactive form by default. If --name is provided, runs non-interactively
and requires --allowance. Example:

  agentdesk agents add --name research-agent --allowance 100 --interval monthly \
      --merchants openai.com,anthropic.com`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireConfig(); err != nil {
			return err
		}
		s, err := store.Load()
		if err != nil {
			return err
		}

		var name, interval string
		var cents int64
		var merchants []string

		nonInteractive := addFlags.Name != ""
		if nonInteractive {
			name, cents, interval, merchants, err = resolveAddFlags(s)
			if err != nil {
				return err
			}
		} else {
			name, cents, interval, merchants, err = promptAddForm(s)
			if err != nil {
				return err
			}
		}

		if !addFlags.JSON {
			fmt.Println(lipgloss.NewStyle().Faint(true).Render("Creating cardholder and virtual card..."))
		}
		cfg, _ := config.LoadOrEmpty()
		if !cfg.Billing.Complete() {
			fmt.Fprintln(os.Stderr, lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(
				"warning: billing details not configured — using fallback identity. Run `agentdesk setup` to fix."))
		}
		ch, err := stripeapi.CreateCardholder(name, cfg.Billing)
		if err != nil {
			return fmt.Errorf("create cardholder: %w", err)
		}
		card, err := stripeapi.CreateVirtualCard(ch.ID, name, cents, interval, merchants)
		if err != nil {
			return fmt.Errorf("create card: %w", err)
		}
		revealed, err := stripeapi.RevealCard(card.ID)
		if err != nil {
			return fmt.Errorf("reveal card: %w", err)
		}

		a := store.Agent{
			Name:             name,
			CardholderID:     ch.ID,
			CardID:           card.ID,
			Last4:            card.Last4,
			Brand:            string(card.Brand),
			Allowance:        store.Allowance{Amount: cents, Interval: interval},
			AllowedMerchants: merchants,
			CreatedAt:        time.Now().UTC(),
		}
		s.Upsert(a)
		if err := s.Save(); err != nil {
			return err
		}

		body := formatCardFile(a, revealed)
		path, err := store.SaveCardFile(name, body)
		if err != nil {
			return err
		}

		if addFlags.JSON {
			out := map[string]any{
				"name":              a.Name,
				"card_id":           a.CardID,
				"cardholder_id":     a.CardholderID,
				"last4":             a.Last4,
				"brand":             a.Brand,
				"number":            revealed.Number,
				"cvc":               revealed.CVC,
				"exp_month":         revealed.ExpMonth,
				"exp_year":          revealed.ExpYear,
				"allowance":         a.Allowance,
				"allowed_merchants": a.AllowedMerchants,
				"card_file":         path,
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
			return nil
		}

		title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42")).Render("✓ Agent created")
		fmt.Println()
		fmt.Println(title)
		fmt.Println(body)
		fmt.Println(lipgloss.NewStyle().Faint(true).Render("Saved to " + path))
		return nil
	},
}

func resolveAddFlags(s *store.Store) (name string, cents int64, interval string, merchants []string, err error) {
	name = addFlags.Name
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]{2,32}$`).MatchString(name) {
		err = fmt.Errorf("--name: letters/digits/_/-, 2–32 chars")
		return
	}
	if s.Find(name) != nil {
		err = fmt.Errorf("agent %q already exists", name)
		return
	}
	if addFlags.Allowance == "" {
		err = fmt.Errorf("--allowance is required in non-interactive mode")
		return
	}
	cents, err = parseMoney(addFlags.Allowance)
	if err != nil {
		err = fmt.Errorf("--allowance: %w", err)
		return
	}
	interval = strings.ToLower(addFlags.Interval)
	if !validIntervals[interval] {
		err = fmt.Errorf("--interval must be daily, weekly, or monthly")
		return
	}
	merchants = splitMerchants(addFlags.Merchants)
	return
}

func promptAddForm(s *store.Store) (name string, cents int64, interval string, merchants []string, err error) {
	form := tui.NewForm("Add agent", []tui.Field{
		{Label: "Agent name", Placeholder: "research-agent", Required: true,
			Validate: func(v string) error {
				if !regexp.MustCompile(`^[a-zA-Z0-9_-]{2,32}$`).MatchString(v) {
					return fmt.Errorf("letters/digits/_/-, 2–32 chars")
				}
				if s.Find(v) != nil {
					return fmt.Errorf("agent %q already exists", v)
				}
				return nil
			}},
		{Label: "Allowance (EUR)", Placeholder: "100.00", Required: true,
			Validate: func(v string) error { _, err := parseMoney(v); return err }},
		{Label: "Interval (daily/weekly/monthly)", Placeholder: "monthly", Value: "monthly", Required: true,
			Validate: func(v string) error {
				if !validIntervals[strings.ToLower(v)] {
					return fmt.Errorf("must be daily, weekly, or monthly")
				}
				return nil
			}},
		{Label: "Allowed merchants (optional, comma-separated)", Placeholder: "openai.com, anthropic.com"},
	})
	finished, err := tea.NewProgram(form).Run()
	if err != nil {
		return
	}
	fm := finished.(tui.FormModel)
	if fm.Cancelled() {
		err = fmt.Errorf("cancelled")
		return
	}
	vals := fm.Values()
	name = vals[0]
	cents, _ = parseMoney(vals[1])
	interval = strings.ToLower(vals[2])
	merchants = splitMerchants(vals[3])
	return
}

var agentsRmCmd = &cobra.Command{
	Use:   "rm [name]",
	Short: "Delete an agent and cancel its card",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireConfig(); err != nil {
			return err
		}
		s, err := store.Load()
		if err != nil {
			return err
		}
		name, err := resolveAgentName(args, s, "Delete which agent?")
		if err != nil {
			return err
		}
		a := s.Find(name)
		if a == nil {
			return fmt.Errorf("no agent named %q", name)
		}
		if err := stripeapi.CancelCard(a.CardID); err != nil {
			return fmt.Errorf("cancel card: %w", err)
		}
		s.Remove(name)
		if err := s.Save(); err != nil {
			return err
		}
		if err := store.RemoveAgentDir(name); err != nil {
			return err
		}
		fmt.Printf("✓ Deleted agent %q (card %s canceled)\n", name, a.CardID)
		return nil
	},
}

var agentsEditCmd = &cobra.Command{
	Use:   "edit [name]",
	Short: "Edit an agent's allowance and allowed merchants",
	Long: `Edit an agent's allowance and allowed merchants.

Runs an interactive form by default. When any of --allowance, --interval, or
--merchants is provided, runs non-interactively. Unset flags preserve the
existing value.

Example:
  agentdesk agents edit research-agent --allowance 250 --interval weekly`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireConfig(); err != nil {
			return err
		}
		s, err := store.Load()
		if err != nil {
			return err
		}
		nameArg := ""
		if len(args) > 0 {
			nameArg = args[0]
		}
		nonInteractive := cmd.Flags().Changed("allowance") ||
			cmd.Flags().Changed("interval") ||
			cmd.Flags().Changed("merchants")
		if nonInteractive && nameArg == "" {
			return fmt.Errorf("agent name required as positional arg in non-interactive mode")
		}

		name, err := resolveAgentName(args, s, "Edit which agent?")
		if err != nil {
			return err
		}
		a := s.Find(name)
		if a == nil {
			return fmt.Errorf("no agent named %q", name)
		}

		var cents int64
		var interval string
		var merchants []string
		if nonInteractive {
			cents = a.Allowance.Amount
			interval = a.Allowance.Interval
			merchants = a.AllowedMerchants
			if cmd.Flags().Changed("allowance") {
				cents, err = parseMoney(editFlags.Allowance)
				if err != nil {
					return fmt.Errorf("--allowance: %w", err)
				}
			}
			if cmd.Flags().Changed("interval") {
				interval = strings.ToLower(editFlags.Interval)
				if !validIntervals[interval] {
					return fmt.Errorf("--interval must be daily, weekly, or monthly")
				}
			}
			if cmd.Flags().Changed("merchants") {
				merchants = splitMerchants(editFlags.Merchants)
			}
		} else {
			form := tui.NewForm(fmt.Sprintf("Edit %s", name), []tui.Field{
				{Label: "Allowance (EUR)", Placeholder: "100.00", Required: true,
					Value:    fmt.Sprintf("%.2f", float64(a.Allowance.Amount)/100),
					Validate: func(v string) error { _, err := parseMoney(v); return err }},
				{Label: "Interval", Placeholder: "monthly", Required: true,
					Value: a.Allowance.Interval,
					Validate: func(v string) error {
						if !validIntervals[strings.ToLower(v)] {
							return fmt.Errorf("must be daily, weekly, or monthly")
						}
						return nil
					}},
				{Label: "Allowed merchants (comma-separated)", Placeholder: "openai.com",
					Value: strings.Join(a.AllowedMerchants, ", ")},
			})
			finished, ferr := tea.NewProgram(form).Run()
			if ferr != nil {
				return ferr
			}
			fm := finished.(tui.FormModel)
			if fm.Cancelled() {
				return fmt.Errorf("cancelled")
			}
			vals := fm.Values()
			cents, _ = parseMoney(vals[0])
			interval = strings.ToLower(vals[1])
			merchants = splitMerchants(vals[2])
		}

		if _, err := stripeapi.UpdateCardSpending(a.CardID, cents, interval, merchants); err != nil {
			return fmt.Errorf("update card: %w", err)
		}
		a.Allowance = store.Allowance{Amount: cents, Interval: interval}
		a.AllowedMerchants = merchants
		s.Upsert(*a)
		if err := s.Save(); err != nil {
			return err
		}
		fmt.Printf("✓ Updated %s — %s\n", name, store.FormatAllowance(a.Allowance))
		return nil
	},
}

// -------- helpers --------

func resolveAgentName(args []string, s *store.Store, prompt string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	if len(s.Agents) == 0 {
		return "", fmt.Errorf("no agents yet")
	}
	if !isTTY() {
		return "", fmt.Errorf("agent name required as positional arg in non-interactive mode")
	}
	names := make([]string, len(s.Agents))
	for i, a := range s.Agents {
		names[i] = a.Name
	}
	p := tui.NewPicker(prompt, names)
	finished, err := tea.NewProgram(p).Run()
	if err != nil {
		return "", err
	}
	pm := finished.(tui.Picker)
	if pm.Cancelled() || pm.Selected() == "" {
		return "", fmt.Errorf("cancelled")
	}
	return pm.Selected(), nil
}

func parseMoney(v string) (int64, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "€")
	v = strings.TrimPrefix(v, "$")
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("expected a positive dollar amount")
	}
	return int64(f * 100), nil
}

func splitMerchants(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func formatCardFile(a store.Agent, revealed *stripe.IssuingCard) string {
	number := revealed.Number
	if number == "" {
		number = "•••• •••• •••• " + revealed.Last4
	} else {
		number = spaceEvery4(number)
	}
	merchants := "any"
	if len(a.AllowedMerchants) > 0 {
		merchants = strings.Join(a.AllowedMerchants, ", ")
	}
	return fmt.Sprintf(`Cardholder:  %s
Card number: %s
Expires:     %02d/%d
CVC:         %s
Policy:      %s
Merchants:   %s
Card ID:     %s
`, a.Name, number, revealed.ExpMonth, revealed.ExpYear, revealed.CVC,
		store.FormatAllowance(a.Allowance), merchants, revealed.ID)
}

func spaceEvery4(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}
