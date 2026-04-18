package cmd

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	stripe "github.com/stripe/stripe-go/v82"

	"github.com/xdamman/agentdesk/internal/rules"
	"github.com/xdamman/agentdesk/internal/store"
	"github.com/xdamman/agentdesk/internal/stripeapi"
	"github.com/xdamman/agentdesk/internal/tui"
)

var (
	requestsJSON     bool
	requestsLimit    int64
	requestShowJSON  bool
	requestActJSON   bool
)

func init() {
	rootCmd.AddCommand(requestsCmd)
	requestsCmd.AddCommand(requestsShowCmd, requestsApproveCmd, requestsDeclineCmd)

	requestsCmd.Flags().BoolVar(&requestsJSON, "json", false, "Print as JSON (non-interactive)")
	requestsCmd.Flags().Int64Var(&requestsLimit, "limit", 25, "Maximum number of requests to list")
	requestsShowCmd.Flags().BoolVar(&requestShowJSON, "json", false, "Print as JSON")
	requestsApproveCmd.Flags().BoolVar(&requestActJSON, "json", false, "Print as JSON")
	requestsDeclineCmd.Flags().BoolVar(&requestActJSON, "json", false, "Print as JSON")
}

var requestsCmd = &cobra.Command{
	Use:   "requests",
	Short: "Show latest authorization requests from agents",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireConfig(); err != nil {
			return err
		}
		auths, err := stripeapi.ListAuthorizations(requestsLimit)
		if err != nil {
			return err
		}
		s, _ := store.Load()

		if requestsJSON {
			out := make([]map[string]any, 0, len(auths))
			for _, a := range auths {
				out = append(out, map[string]any{
					"id":       a.ID,
					"created":  a.Created,
					"agent":    agentForAuth(s, a),
					"merchant": merchantName(a),
					"amount":   a.Amount,
					"currency": strings.ToUpper(string(a.Currency)),
					"status":   stripeapi.FormatAuthorizationStatus(a),
					"approved": a.Approved,
				})
			}
			return printJSON(out)
		}

		if len(auths) == 0 {
			fmt.Println("No requests yet. Agents' card charges will appear here.")
			return nil
		}

		cols, rows := authTable(s, auths)
		if !interactive(requestsJSON) {
			printPlainTable(cols, rows)
			return nil
		}

		tm := tui.NewTable("Requests", cols, rows).WithActions(
			tui.TableAction{Key: "a", Label: "accept"},
			tui.TableAction{Key: "d", Label: "decline"},
		)
		finished, err := tea.NewProgram(tm).Run()
		if err != nil {
			return err
		}
		fm := finished.(tui.TableModel)
		switch fm.Action() {
		case "a":
			return doApprove(fm.Selected())
		case "d":
			return doDecline(fm.Selected())
		}
		return nil
	},
}

// authTable builds the shared columns/rows for authorization listings.
func authTable(s *store.Store, auths []*stripe.IssuingAuthorization) ([]table.Column, []table.Row) {
	cols := []table.Column{
		{Title: "ID", Width: 32},
		{Title: "WHEN", Width: 13},
		{Title: "AGENT", Width: 16},
		{Title: "MERCHANT", Width: 20},
		{Title: "AMOUNT", Width: 10},
		{Title: "STATUS", Width: 10},
	}
	rows := make([]table.Row, 0, len(auths))
	for _, a := range auths {
		rows = append(rows, table.Row{
			a.ID,
			time.Unix(a.Created, 0).Format("Jan 02 15:04"),
			agentForAuth(s, a),
			merchantName(a),
			fmt.Sprintf("€%.2f", float64(a.Amount)/100),
			stripeapi.FormatAuthorizationStatus(a),
		})
	}
	return cols, rows
}

// pickRequestID runs a picker table and returns the selected authorization id,
// or "" if cancelled. Used when approve/decline is invoked without an id arg.
func pickRequestID(prompt string) (string, error) {
	auths, err := stripeapi.ListAuthorizations(25)
	if err != nil {
		return "", err
	}
	if len(auths) == 0 {
		return "", fmt.Errorf("no authorization requests found")
	}
	s, _ := store.Load()
	cols, rows := authTable(s, auths)
	tm := tui.NewTable(prompt, cols, rows)
	finished, err := tea.NewProgram(tm).Run()
	if err != nil {
		return "", err
	}
	id := finished.(tui.TableModel).Selected()
	if id == "" {
		return "", fmt.Errorf("cancelled")
	}
	return id, nil
}

var requestsShowCmd = &cobra.Command{
	Use:   "show [requestId]",
	Short: "Show details about a request",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireConfig(); err != nil {
			return err
		}
		a, err := stripeapi.GetAuthorization(args[0])
		if err != nil {
			return err
		}
		s, _ := store.Load()
		if requestShowJSON {
			return printJSON(authJSON(s, a))
		}
		fmt.Println(renderAuth(s, a))
		return nil
	},
}

var requestsApproveCmd = &cobra.Command{
	Use:   "approve [requestId]",
	Short: "Approve a pending authorization (saves an auto-approve rule if expired)",
	Long: `Approve a pending Stripe Issuing authorization.

If no requestId is given, shows a picker of recent authorizations.

Stripe only holds an authorization as "pending" for 2 seconds after the
issuing_authorization.request webhook fires. If the window has already closed,
Stripe returns an error. In that case, agentdesk saves a rule
(card + merchant + amount + date) so that the next matching request is
auto-approved by 'agentdesk daemon' within the window.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireConfig(); err != nil {
			return err
		}
		id, err := requestIDFromArgs(args, "Approve which request?")
		if err != nil {
			return err
		}
		return doApprove(id)
	},
}

func requestIDFromArgs(args []string, prompt string) (string, error) {
	if len(args) == 1 {
		return args[0], nil
	}
	if !isTTY() {
		return "", fmt.Errorf("requestId required as positional arg in non-interactive mode")
	}
	return pickRequestID(prompt)
}

func doApprove(id string) error {
	a, err := stripeapi.ApproveAuthorization(id)
	if err == nil {
		if requestActJSON {
			s, _ := store.Load()
			return printJSON(authJSON(s, a))
		}
		fmt.Printf("✓ Approved %s — €%.2f @ %s\n", a.ID, float64(a.Amount)/100, merchantName(a))
		return nil
	}

	// If the Stripe 2-second window has closed, save an auto-approve rule so
	// the daemon catches the next identical request in time, and surface a
	// RuleCreatedError so the CLI exits non-zero with a helpful message.
	if !isApprovalWindowClosed(err) {
		return err
	}
	auth, getErr := stripeapi.GetAuthorization(id)
	if getErr != nil {
		return err
	}
	rule, created, saveErr := saveAutoApproveRule(auth)
	if saveErr != nil {
		return fmt.Errorf("%w (and failed to save auto-approve rule: %v)", err, saveErr)
	}
	return &RuleCreatedError{
		AuthID:      auth.ID,
		Status:      stripeapi.FormatAuthorizationStatus(auth),
		RuleCreated: created,
		Rule:        rule,
	}
}

// isApprovalWindowClosed reports whether a Stripe approve/decline error means
// the 2-second real-time authorization window has already passed.
func isApprovalWindowClosed(err error) bool {
	var se *stripe.Error
	if !errors.As(err, &se) {
		return false
	}
	if se.HTTPStatusCode != 400 {
		return false
	}
	msg := strings.ToLower(se.Msg)
	return strings.Contains(msg, "no longer pending") ||
		strings.Contains(msg, "period for acting")
}

func saveAutoApproveRule(a *stripe.IssuingAuthorization) (*rules.Rule, bool, error) {
	rs, err := rules.Load()
	if err != nil {
		return nil, false, err
	}
	agents, _ := store.Load()
	agentName := ""
	cardID := ""
	if a.Card != nil {
		cardID = a.Card.ID
		if ag := agents.FindByCard(a.Card.ID); ag != nil {
			agentName = ag.Name
		}
	}
	merchant := merchantName(a)
	r, created := rs.Add(rules.Rule{
		Agent:    agentName,
		CardID:   cardID,
		Merchant: merchant,
		Amount:   resolveAuthAmount(a),
		Currency: strings.ToLower(string(a.Currency)),
		Date:     rules.DateFromUnix(a.Created),
	})
	if created {
		if err := rs.Save(); err != nil {
			return nil, false, err
		}
	}
	return r, created, nil
}

var requestsDeclineCmd = &cobra.Command{
	Use:   "decline [requestId]",
	Short: "Decline a pending authorization",
	Long:  "Decline a pending Stripe Issuing authorization. If no requestId is given, shows a picker of recent authorizations.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireConfig(); err != nil {
			return err
		}
		id, err := requestIDFromArgs(args, "Decline which request?")
		if err != nil {
			return err
		}
		return doDecline(id)
	},
}

func doDecline(id string) error {
	a, err := stripeapi.DeclineAuthorization(id)
	if err != nil {
		return err
	}
	if requestActJSON {
		s, _ := store.Load()
		return printJSON(authJSON(s, a))
	}
	fmt.Printf("✗ Declined %s — €%.2f @ %s\n", a.ID, float64(a.Amount)/100, merchantName(a))
	return nil
}

func authJSON(s *store.Store, a *stripe.IssuingAuthorization) map[string]any {
	out := map[string]any{
		"id":       a.ID,
		"created":  a.Created,
		"agent":    agentForAuth(s, a),
		"amount":   a.Amount,
		"currency": strings.ToUpper(string(a.Currency)),
		"merchant": merchantName(a),
		"status":   stripeapi.FormatAuthorizationStatus(a),
		"approved": a.Approved,
	}
	if a.Card != nil {
		out["card_id"] = a.Card.ID
		out["last4"] = a.Card.Last4
	}
	if a.MerchantData != nil {
		out["merchant_category"] = a.MerchantData.Category
		out["merchant_country"] = a.MerchantData.Country
		out["merchant_city"] = a.MerchantData.City
		out["merchant_url"] = a.MerchantData.URL
	}
	return out
}

func agentForAuth(s *store.Store, a *stripe.IssuingAuthorization) string {
	if a.Card != nil {
		if found := s.FindByCard(a.Card.ID); found != nil {
			return found.Name
		}
		if a.Card.Metadata != nil {
			if v, ok := a.Card.Metadata["agentdesk_agent"]; ok {
				return v
			}
		}
	}
	if a.Cardholder != nil && a.Cardholder.Name != "" {
		return a.Cardholder.Name
	}
	return "—"
}

func merchantName(a *stripe.IssuingAuthorization) string {
	if a.MerchantData != nil {
		if a.MerchantData.Name != "" {
			return a.MerchantData.Name
		}
		if a.MerchantData.URL != "" {
			return a.MerchantData.URL
		}
	}
	return "—"
}

var (
	authHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render
	authLabel  = lipgloss.NewStyle().Bold(true).Render
	authFaint  = lipgloss.NewStyle().Faint(true).Render
)

func renderAuth(s *store.Store, a *stripe.IssuingAuthorization) string {
	var b strings.Builder
	b.WriteString(authHeader("Request " + a.ID))
	b.WriteString("\n")
	fmt.Fprintf(&b, "%s %s\n", authLabel("When:    "), time.Unix(a.Created, 0).Format(time.RFC1123))
	fmt.Fprintf(&b, "%s %s\n", authLabel("Agent:   "), agentForAuth(s, a))
	if a.Card != nil {
		fmt.Fprintf(&b, "%s %s (••%s)\n", authLabel("Card:    "), a.Card.ID, a.Card.Last4)
	}
	fmt.Fprintf(&b, "%s €%.2f %s\n", authLabel("Amount:  "), float64(a.Amount)/100, strings.ToUpper(string(a.Currency)))
	fmt.Fprintf(&b, "%s %s\n", authLabel("Status:  "), stripeapi.FormatAuthorizationStatus(a))
	if a.MerchantData != nil {
		md := a.MerchantData
		fmt.Fprintf(&b, "%s %s\n", authLabel("Vendor:  "), md.Name)
		if md.Category != "" {
			fmt.Fprintf(&b, "%s %s\n", authLabel("Category:"), md.Category)
		}
		loc := strings.TrimSpace(strings.Join([]string{md.City, md.State, md.Country}, ", "))
		if loc != "" && loc != ", , " {
			fmt.Fprintf(&b, "%s %s\n", authLabel("Location:"), loc)
		}
		if md.URL != "" {
			fmt.Fprintf(&b, "%s %s\n", authLabel("URL:     "), md.URL)
		}
	}
	if a.Status == stripe.IssuingAuthorizationStatusPending {
		b.WriteString("\n")
		b.WriteString(authFaint(fmt.Sprintf(
			"Approve: agentdesk requests approve %s\nDecline: agentdesk requests decline %s",
			a.ID, a.ID,
		)))
	}
	return b.String()
}
