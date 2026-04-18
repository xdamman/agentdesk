package cmd

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/nbd-wtf/go-nostr/nip05"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/spf13/cobra"

	"github.com/xdamman/agentdesk/internal/config"
	"github.com/xdamman/agentdesk/internal/tui"
)

var setupFlags struct {
	APIKey     string
	FirstName  string
	LastName   string
	DOB        string
	Email      string
	Phone      string
	Line1      string
	Line2      string
	City       string
	State      string
	PostalCode string
	Country    string
	Admin      string
	Show       bool
}

func init() {
	rootCmd.AddCommand(setupCmd)
	setupCmd.Flags().StringVar(&setupFlags.APIKey, "api-key", "", "Stripe secret key (or set STRIPE_API_KEY)")
	setupCmd.Flags().StringVar(&setupFlags.FirstName, "first-name", "", "Default cardholder first name")
	setupCmd.Flags().StringVar(&setupFlags.LastName, "last-name", "", "Default cardholder last name")
	setupCmd.Flags().StringVar(&setupFlags.DOB, "dob", "", "Default cardholder date of birth (YYYY-MM-DD)")
	setupCmd.Flags().StringVar(&setupFlags.Email, "email", "", "Default cardholder email")
	setupCmd.Flags().StringVar(&setupFlags.Phone, "phone", "", "Default cardholder phone (E.164, e.g. +33612345678)")
	setupCmd.Flags().StringVar(&setupFlags.Line1, "address-line1", "", "Default billing address line 1")
	setupCmd.Flags().StringVar(&setupFlags.Line2, "address-line2", "", "Default billing address line 2")
	setupCmd.Flags().StringVar(&setupFlags.City, "city", "", "Default billing city")
	setupCmd.Flags().StringVar(&setupFlags.State, "state", "", "Default billing state/region")
	setupCmd.Flags().StringVar(&setupFlags.PostalCode, "postal-code", "", "Default billing postal code")
	setupCmd.Flags().StringVar(&setupFlags.Country, "country", "", "Default billing country (ISO 3166-1 alpha-2, e.g. FR)")
	setupCmd.Flags().StringVar(&setupFlags.Admin, "admin", "", "Admin Nostr identity: npub, hex pubkey, or NIP-05 address (name@domain)")
	setupCmd.Flags().BoolVar(&setupFlags.Show, "show", false, "Print current settings and exit")
}

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Configure your Stripe API key and default cardholder details",
	Long: `Configure your Stripe API key and the default identity + billing address used
when creating new agents.

Interactive:
  agentdesk setup

Non-interactive (any subset of flags):
  agentdesk setup --api-key sk_test_xxx
  agentdesk setup --first-name Ada --last-name Lovelace --dob 1815-12-10 \
    --address-line1 "8 rue de Londres" --city Paris --postal-code 75009 --country FR

Inspect:
  agentdesk setup --show`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := config.LoadOrEmpty()
		if err != nil {
			return err
		}

		if setupFlags.Show {
			printSettings(c)
			return nil
		}

		changed, err := applySetupFlags(cmd, c)
		if err != nil {
			return err
		}
		if changed {
			return config.Save(c)
		}

		// No flags provided → interactive menu or first-time onboarding.
		if c.StripeAPIKey == "" {
			// First time: pick up STRIPE_API_KEY env var if present, else prompt.
			if env := strings.TrimSpace(os.Getenv("STRIPE_API_KEY")); env != "" {
				if !strings.HasPrefix(env, "sk_") {
					return fmt.Errorf("STRIPE_API_KEY: expected a Stripe secret key starting with sk_")
				}
				c.StripeAPIKey = env
			} else {
				key, err := promptAPIKey("")
				if err != nil {
					return err
				}
				c.StripeAPIKey = key
			}
			if err := config.Save(c); err != nil {
				return err
			}
			fmt.Println(lipgloss.NewStyle().Faint(true).Render("Next: set your default cardholder details."))
			if err := editBillingInteractive(c); err != nil {
				return err
			}
			return config.Save(c)
		}

		// Returning user: show menu.
		return interactiveMenu(c)
	},
}

func applySetupFlags(cmd *cobra.Command, c *config.Config) (bool, error) {
	changed := false
	if apiKey := strings.TrimSpace(setupFlags.APIKey); apiKey != "" {
		if !strings.HasPrefix(apiKey, "sk_") {
			return false, fmt.Errorf("--api-key: expected a Stripe secret key starting with sk_")
		}
		c.StripeAPIKey = apiKey
		changed = true
	}

	billingFlags := []string{"first-name", "last-name", "dob", "email", "phone",
		"address-line1", "address-line2", "city", "state", "postal-code", "country"}
	touched := false
	for _, f := range billingFlags {
		if cmd.Flags().Changed(f) {
			touched = true
			break
		}
	}
	if touched {
		b := c.Billing
		if b == nil {
			b = &config.Billing{}
		}
		if cmd.Flags().Changed("first-name") {
			b.FirstName = setupFlags.FirstName
		}
		if cmd.Flags().Changed("last-name") {
			b.LastName = setupFlags.LastName
		}
		if cmd.Flags().Changed("dob") {
			if _, _, _, ok := config.ParseDOB(setupFlags.DOB); !ok {
				return false, fmt.Errorf("--dob: expected YYYY-MM-DD")
			}
			b.DOB = setupFlags.DOB
		}
		if cmd.Flags().Changed("email") {
			b.Email = setupFlags.Email
		}
		if cmd.Flags().Changed("phone") {
			b.PhoneNumber = setupFlags.Phone
		}
		if cmd.Flags().Changed("address-line1") {
			b.Line1 = setupFlags.Line1
		}
		if cmd.Flags().Changed("address-line2") {
			b.Line2 = setupFlags.Line2
		}
		if cmd.Flags().Changed("city") {
			b.City = setupFlags.City
		}
		if cmd.Flags().Changed("state") {
			b.State = setupFlags.State
		}
		if cmd.Flags().Changed("postal-code") {
			b.PostalCode = setupFlags.PostalCode
		}
		if cmd.Flags().Changed("country") {
			cc := strings.ToUpper(setupFlags.Country)
			if !regexp.MustCompile(`^[A-Z]{2}$`).MatchString(cc) {
				return false, fmt.Errorf("--country: expected 2-letter ISO code (e.g. FR)")
			}
			b.Country = cc
		}
		c.Billing = b
		changed = true
	}

	if cmd.Flags().Changed("admin") {
		admin := strings.TrimSpace(setupFlags.Admin)
		if admin == "" {
			c.AdminNostrPubkey = ""
			c.AdminNIP05 = ""
		} else {
			hex, display, err := resolveNostrIdentity(admin)
			if err != nil {
				return false, fmt.Errorf("--admin: %w", err)
			}
			c.AdminNostrPubkey = hex
			c.AdminNIP05 = display
		}
		changed = true
	}
	return changed, nil
}

// resolveNostrIdentity accepts an npub, a 64-char hex pubkey, or a NIP-05
// address and returns the hex pubkey plus a display identifier.
func resolveNostrIdentity(input string) (hex, display string, err error) {
	input = strings.TrimSpace(input)
	switch {
	case strings.HasPrefix(input, "npub1"):
		prefix, decoded, dErr := nip19.Decode(input)
		if dErr != nil || prefix != "npub" {
			return "", "", fmt.Errorf("invalid npub")
		}
		h, ok := decoded.(string)
		if !ok {
			return "", "", fmt.Errorf("unexpected decoded type %T", decoded)
		}
		return h, input, nil
	case strings.Contains(input, "@"):
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		pp, qErr := nip05.QueryIdentifier(ctx, input)
		if qErr != nil {
			return "", "", fmt.Errorf("NIP-05 lookup failed: %w", qErr)
		}
		if pp == nil || pp.PublicKey == "" {
			return "", "", fmt.Errorf("NIP-05 %q did not resolve to a pubkey", input)
		}
		return pp.PublicKey, input, nil
	case len(input) == 64 && isHex(input):
		return strings.ToLower(input), input, nil
	}
	return "", "", fmt.Errorf("expected npub1…, hex pubkey, or NIP-05 address (name@domain)")
}

func isHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// ---- interactive menu ----

func interactiveMenu(c *config.Config) error {
	printSettings(c)
	fmt.Println()
	choice := promptMenuChoice()
	switch choice {
	case "Stripe API key":
		key, err := promptAPIKey(mask(c.StripeAPIKey))
		if err != nil {
			return err
		}
		c.StripeAPIKey = key
	case "Billing & identity":
		if err := editBillingInteractive(c); err != nil {
			return err
		}
	case "Admin approver (Nostr)":
		hex, display, err := promptAdminInteractive(c)
		if err != nil {
			return err
		}
		c.AdminNostrPubkey = hex
		c.AdminNIP05 = display
	case "":
		return nil
	}
	return config.Save(c)
}

func promptMenuChoice() string {
	items := []string{"Stripe API key", "Billing & identity", "Admin approver (Nostr)", "Quit"}
	p := tui.NewPicker("What would you like to configure?", items)
	finished, err := tea.NewProgram(p).Run()
	if err != nil {
		return ""
	}
	pm := finished.(tui.Picker)
	if pm.Cancelled() || pm.Selected() == "Quit" {
		return ""
	}
	return pm.Selected()
}

func promptAdminInteractive(c *config.Config) (hex, display string, err error) {
	prefill := c.AdminNIP05
	if prefill == "" && c.AdminNostrPubkey != "" {
		if np, eErr := nip19.EncodePublicKey(c.AdminNostrPubkey); eErr == nil {
			prefill = np
		}
	}
	form := tui.NewForm("Admin approver", []tui.Field{
		{
			Label:       "npub, hex pubkey, or NIP-05 address",
			Placeholder: "npub1… | alice@example.com | (leave blank to clear)",
			Value:       prefill,
			Validate: func(v string) error {
				if strings.TrimSpace(v) == "" {
					return nil
				}
				_, _, e := resolveNostrIdentity(v)
				return e
			},
		},
	})
	finished, rErr := tea.NewProgram(form).Run()
	if rErr != nil {
		return "", "", rErr
	}
	fm := finished.(tui.FormModel)
	if fm.Cancelled() {
		return c.AdminNostrPubkey, c.AdminNIP05, nil
	}
	val := strings.TrimSpace(fm.Values()[0])
	if val == "" {
		return "", "", nil
	}
	return resolveNostrIdentity(val)
}

func promptAPIKey(prefill string) (string, error) {
	m := newSetupModel(prefill)
	finished, err := tea.NewProgram(m).Run()
	if err != nil {
		return "", err
	}
	fm := finished.(setupModel)
	if fm.cancelled {
		return "", fmt.Errorf("cancelled")
	}
	key := strings.TrimSpace(fm.input.Value())
	if !strings.HasPrefix(key, "sk_") {
		return "", fmt.Errorf("expected a Stripe secret key starting with sk_")
	}
	return key, nil
}

func editBillingInteractive(c *config.Config) error {
	b := c.Billing
	if b == nil {
		b = &config.Billing{Country: "FR"}
	}
	form := tui.NewForm("Default cardholder details", []tui.Field{
		{Label: "First name", Placeholder: "Ada", Value: b.FirstName, Required: true,
			Validate: validatePersonName},
		{Label: "Last name", Placeholder: "Lovelace", Value: b.LastName, Required: true,
			Validate: validatePersonName},
		{Label: "Date of birth (YYYY-MM-DD)", Placeholder: "1985-07-24", Value: b.DOB, Required: true,
			Validate: func(v string) error {
				if _, _, _, ok := config.ParseDOB(v); !ok {
					return fmt.Errorf("expected YYYY-MM-DD")
				}
				return nil
			}},
		{Label: "Email (optional)", Placeholder: "you@example.com", Value: b.Email},
		{Label: "Phone (optional, E.164)", Placeholder: "+33612345678", Value: b.PhoneNumber},
		{Label: "Address line 1", Placeholder: "8 rue de Londres", Value: b.Line1, Required: true},
		{Label: "Address line 2 (optional)", Value: b.Line2},
		{Label: "City", Placeholder: "Paris", Value: b.City, Required: true},
		{Label: "State/Region (optional)", Value: b.State},
		{Label: "Postal code", Placeholder: "75009", Value: b.PostalCode, Required: true},
		{Label: "Country (ISO 3166-1 alpha-2)", Placeholder: "FR", Value: orDefault(b.Country, "FR"), Required: true,
			Validate: func(v string) error {
				if !regexp.MustCompile(`^[A-Za-z]{2}$`).MatchString(v) {
					return fmt.Errorf("expected 2-letter ISO code")
				}
				return nil
			}},
	})
	finished, err := tea.NewProgram(form).Run()
	if err != nil {
		return err
	}
	fm := finished.(tui.FormModel)
	if fm.Cancelled() {
		return fmt.Errorf("cancelled")
	}
	v := fm.Values()
	c.Billing = &config.Billing{
		FirstName:   v[0],
		LastName:    v[1],
		DOB:         v[2],
		Email:       v[3],
		PhoneNumber: v[4],
		Line1:       v[5],
		Line2:       v[6],
		City:        v[7],
		State:       v[8],
		PostalCode:  v[9],
		Country:     strings.ToUpper(v[10]),
	}
	return nil
}

func validatePersonName(v string) error {
	if !regexp.MustCompile(`^[A-Za-zÀ-ÖØ-öø-ÿ'’\- .]+$`).MatchString(v) {
		return fmt.Errorf("no digits or special chars except - ' space")
	}
	return nil
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// ---- settings printer ----

func printSettings(c *config.Config) {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render
	label := lipgloss.NewStyle().Bold(true).Render
	faint := lipgloss.NewStyle().Faint(true).Render

	fmt.Println(title("agentdesk settings"))
	fmt.Println()
	if c.StripeAPIKey == "" {
		fmt.Println(label("Stripe API key: ") + faint("(not set)"))
	} else {
		fmt.Println(label("Stripe API key: ") + mask(c.StripeAPIKey))
	}

	if c.Billing == nil {
		fmt.Println(label("Billing:        ") + faint("(not set)"))
		return
	}
	b := c.Billing
	fmt.Println(label("Billing:"))
	fmt.Printf("  %s %s %s\n", label("Name:     "), b.FirstName, b.LastName)
	fmt.Printf("  %s %s\n", label("DOB:      "), orBlank(b.DOB))
	if b.Email != "" {
		fmt.Printf("  %s %s\n", label("Email:    "), b.Email)
	}
	if b.PhoneNumber != "" {
		fmt.Printf("  %s %s\n", label("Phone:    "), b.PhoneNumber)
	}
	addr := strings.TrimSpace(strings.Join([]string{b.Line1, b.Line2, b.City, b.State, b.PostalCode, b.Country}, ", "))
	fmt.Printf("  %s %s\n", label("Address:  "), orBlank(addr))

	fmt.Println()
	if c.AdminNostrPubkey == "" {
		fmt.Println(label("Admin approver: ") + orBlank(""))
	} else {
		display := c.AdminNIP05
		if display == "" {
			if np, err := nip19.EncodePublicKey(c.AdminNostrPubkey); err == nil {
				display = np
			} else {
				display = c.AdminNostrPubkey
			}
		}
		fmt.Println(label("Admin approver: ") + display)
	}
}

func orBlank(s string) string {
	if s == "" {
		return lipgloss.NewStyle().Faint(true).Render("(not set)")
	}
	return s
}

// ---- API key input model (unchanged) ----

type setupModel struct {
	input     textinput.Model
	cancelled bool
	done      bool
}

func newSetupModel(placeholder string) setupModel {
	ti := textinput.New()
	ti.Placeholder = "sk_test_..."
	if placeholder != "" {
		ti.Placeholder = placeholder + " (enter new value or press Enter to keep)"
	}
	ti.Focus()
	ti.CharLimit = 200
	ti.Width = 60
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '•'
	return setupModel{input: ti}
}

func (m setupModel) Init() tea.Cmd { return textinput.Blink }

func (m setupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEnter:
			m.done = true
			return m, tea.Quit
		case tea.KeyCtrlC, tea.KeyEsc:
			m.cancelled = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

var setupTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render
var setupHint = lipgloss.NewStyle().Faint(true).Render

func (m setupModel) View() string {
	if m.done || m.cancelled {
		return ""
	}
	return fmt.Sprintf(
		"%s\n\n%s\n\n%s",
		setupTitle("Stripe API key"),
		m.input.View(),
		setupHint("Paste your Stripe secret key · Enter to save · Esc to cancel"),
	)
}

func mask(key string) string {
	if len(key) < 8 {
		return "••••"
	}
	return key[:7] + strings.Repeat("•", 8) + key[len(key)-4:]
}
