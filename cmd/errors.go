package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	stripe "github.com/stripe/stripe-go/v82"

	"github.com/xdamman/agentdesk/internal/rules"
)

// RuleCreatedError is returned when an approve call fails because Stripe's
// 2-second authorization window has already closed, and agentdesk saved (or
// found an existing) auto-approve rule so the next matching request can be
// handled by `agentdesk daemon`. Callers should return this instead of a
// plain error so the CLI exits non-zero while still conveying the recovery.
type RuleCreatedError struct {
	AuthID      string
	Status      string
	RuleCreated bool
	Rule        *rules.Rule
}

func (e *RuleCreatedError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s is %s; Stripe's 2-second approval window has closed.\n",
		e.AuthID, e.Status)
	if e.RuleCreated {
		fmt.Fprintf(&b, "Saved auto-approve rule %s: €%.2f @ %s on %s.\n",
			e.Rule.ID, float64(e.Rule.Amount)/100, e.Rule.Merchant, e.Rule.Date)
	} else if e.Rule != nil {
		fmt.Fprintf(&b, "A matching auto-approve rule already exists (%s).\n", e.Rule.ID)
	}
	b.WriteString("Run `agentdesk daemon` to catch the next matching request in time.")
	return b.String()
}

// PrintError writes err to stderr. In plain mode it prints just the human
// message (unwrapping *stripe.Error so we don't dump its JSON blob). In JSON
// mode it emits the structured Stripe error object (or a minimal wrapper for
// non-Stripe errors).
func PrintError(err error) {
	if err == nil {
		return
	}
	var rce *RuleCreatedError
	if errors.As(err, &rce) {
		if wantsJSON() {
			payload := map[string]any{
				"error":        "authorization not pending — 2s window already closed",
				"auth_id":      rce.AuthID,
				"status":       rce.Status,
				"rule_created": rce.RuleCreated,
				"rule":         rce.Rule,
			}
			b, _ := json.MarshalIndent(payload, "", "  ")
			fmt.Fprintln(os.Stderr, string(b))
			return
		}
		fmt.Fprintln(os.Stderr, rce.Error())
		return
	}

	var se *stripe.Error
	isStripe := errors.As(err, &se)

	if wantsJSON() {
		var payload any
		if isStripe {
			payload = se
		} else {
			payload = map[string]string{"error": err.Error()}
		}
		b, mErr := json.MarshalIndent(payload, "", "  ")
		if mErr != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return
		}
		fmt.Fprintln(os.Stderr, string(b))
		return
	}

	if isStripe && se.Msg != "" {
		// Preserve any outer wrap prefix ("create card: ...") added via %w.
		outer := err.Error()
		stripeStr := se.Error()
		if idx := strings.LastIndex(outer, stripeStr); idx > 0 {
			prefix := strings.TrimSuffix(outer[:idx], ": ")
			if prefix != "" {
				fmt.Fprintln(os.Stderr, prefix+": "+se.Msg)
				return
			}
		}
		fmt.Fprintln(os.Stderr, se.Msg)
		return
	}
	fmt.Fprintln(os.Stderr, err.Error())
}

// wantsJSON reports whether any invoked subcommand had its --json flag set.
// Only one command runs per invocation, so at most one of these is ever true.
func wantsJSON() bool {
	return addFlags.JSON ||
		cardsJSON ||
		agentsJSON ||
		requestsJSON ||
		requestShowJSON ||
		requestActJSON
}
