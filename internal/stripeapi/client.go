package stripeapi

import (
	"fmt"
	"time"

	stripe "github.com/stripe/stripe-go/v82"
	issauth "github.com/stripe/stripe-go/v82/issuing/authorization"
	isscard "github.com/stripe/stripe-go/v82/issuing/card"
	isscardholder "github.com/stripe/stripe-go/v82/issuing/cardholder"
	whendpoint "github.com/stripe/stripe-go/v82/webhookendpoint"

	"github.com/xdamman/agentdesk/internal/config"
)

// Init configures the global Stripe API key and silences the SDK's built-in
// logger (we format errors ourselves in cmd/errors.go).
func Init(apiKey string) {
	stripe.Key = apiKey
	stripe.DefaultLeveledLogger = &stripe.LeveledLogger{Level: stripe.LevelNull}
}

// CreateCardholder creates a new Issuing cardholder for an agent, using the
// default identity + billing details from config. Falls back to a minimal FR
// default if billing hasn't been configured yet.
func CreateCardholder(agentName string, billing *config.Billing) (*stripe.IssuingCardholder, error) {
	b := billing
	if b == nil {
		b = &config.Billing{
			FirstName: "Agent", LastName: "Desk",
			DOB:        "1990-01-01",
			Line1:      "8 rue de Londres",
			City:       "Paris",
			PostalCode: "75009",
			Country:    "FR",
		}
	}
	email := b.Email
	if email == "" {
		email = fmt.Sprintf("%s@agentdesk.local", agentName)
	}
	phone := b.PhoneNumber
	if phone == "" {
		phone = "+33123456789"
	}

	addr := &stripe.AddressParams{
		Line1:      stripe.String(b.Line1),
		City:       stripe.String(b.City),
		PostalCode: stripe.String(b.PostalCode),
		Country:    stripe.String(b.Country),
	}
	if b.Line2 != "" {
		addr.Line2 = stripe.String(b.Line2)
	}
	if b.State != "" {
		addr.State = stripe.String(b.State)
	}

	individual := &stripe.IssuingCardholderIndividualParams{
		FirstName: stripe.String(b.FirstName),
		LastName:  stripe.String(b.LastName),
	}
	if d, m, y, ok := config.ParseDOB(b.DOB); ok {
		individual.DOB = &stripe.IssuingCardholderIndividualDOBParams{
			Day:   stripe.Int64(int64(d)),
			Month: stripe.Int64(int64(m)),
			Year:  stripe.Int64(int64(y)),
		}
	}

	params := &stripe.IssuingCardholderParams{
		Name:        stripe.String(agentName),
		Email:       stripe.String(email),
		PhoneNumber: stripe.String(phone),
		Type:        stripe.String("individual"),
		Individual:  individual,
		Billing:     &stripe.IssuingCardholderBillingParams{Address: addr},
	}
	params.AddMetadata("agentdesk_agent", agentName)
	return isscardholder.New(params)
}

// CreateVirtualCard creates a virtual Issuing card with spending controls.
func CreateVirtualCard(cardholderID, agentName string, amount int64, interval string, merchants []string) (*stripe.IssuingCard, error) {
	limitParams := &stripe.IssuingCardSpendingControlsSpendingLimitParams{
		Amount:   stripe.Int64(amount),
		Interval: stripe.String(interval),
	}
	controls := &stripe.IssuingCardSpendingControlsParams{
		SpendingLimits: []*stripe.IssuingCardSpendingControlsSpendingLimitParams{limitParams},
	}
	params := &stripe.IssuingCardParams{
		Cardholder:       stripe.String(cardholderID),
		Currency:         stripe.String("eur"),
		Type:             stripe.String("virtual"),
		Status:           stripe.String("active"),
		SpendingControls: controls,
	}
	params.AddMetadata("agentdesk_agent", agentName)
	if len(merchants) > 0 {
		params.AddMetadata("agentdesk_allowed_merchants", joinMerchants(merchants))
	}
	return isscard.New(params)
}

// RevealCard retrieves a card with full PAN and CVC expanded.
func RevealCard(cardID string) (*stripe.IssuingCard, error) {
	params := &stripe.IssuingCardParams{}
	params.AddExpand("number")
	params.AddExpand("cvc")
	return isscard.Get(cardID, params)
}

// UpdateCardSpending updates an existing card's spending limit and allowed merchants metadata.
func UpdateCardSpending(cardID string, amount int64, interval string, merchants []string) (*stripe.IssuingCard, error) {
	limitParams := &stripe.IssuingCardSpendingControlsSpendingLimitParams{
		Amount:   stripe.Int64(amount),
		Interval: stripe.String(interval),
	}
	params := &stripe.IssuingCardParams{
		SpendingControls: &stripe.IssuingCardSpendingControlsParams{
			SpendingLimits: []*stripe.IssuingCardSpendingControlsSpendingLimitParams{limitParams},
		},
	}
	if len(merchants) > 0 {
		params.AddMetadata("agentdesk_allowed_merchants", joinMerchants(merchants))
	} else {
		params.AddMetadata("agentdesk_allowed_merchants", "")
	}
	return isscard.Update(cardID, params)
}

// CancelCard sets the card status to canceled.
func CancelCard(cardID string) error {
	params := &stripe.IssuingCardParams{
		Status: stripe.String("canceled"),
	}
	_, err := isscard.Update(cardID, params)
	return err
}

// ListCards lists all Issuing cards on the account.
func ListCards() ([]*stripe.IssuingCard, error) {
	params := &stripe.IssuingCardListParams{}
	params.Limit = stripe.Int64(100)
	it := isscard.List(params)
	var out []*stripe.IssuingCard
	for it.Next() {
		out = append(out, it.IssuingCard())
	}
	return out, it.Err()
}

// ListAuthorizations lists recent Issuing authorizations, newest first.
func ListAuthorizations(limit int64) ([]*stripe.IssuingAuthorization, error) {
	params := &stripe.IssuingAuthorizationListParams{}
	params.Limit = stripe.Int64(limit)
	it := issauth.List(params)
	var out []*stripe.IssuingAuthorization
	for it.Next() {
		out = append(out, it.IssuingAuthorization())
	}
	return out, it.Err()
}

// GetAuthorization fetches a single authorization.
func GetAuthorization(id string) (*stripe.IssuingAuthorization, error) {
	return issauth.Get(id, nil)
}

// ApproveAuthorization approves a pending authorization.
func ApproveAuthorization(id string) (*stripe.IssuingAuthorization, error) {
	return issauth.Approve(id, &stripe.IssuingAuthorizationApproveParams{})
}

// DeclineAuthorization declines a pending authorization.
func DeclineAuthorization(id string) (*stripe.IssuingAuthorization, error) {
	return issauth.Decline(id, &stripe.IssuingAuthorizationDeclineParams{})
}

// SpentThisPeriod sums approved authorization amounts for a card within the
// card's spending-limit interval window (daily/weekly/monthly).
func SpentThisPeriod(cardID, interval string) (int64, error) {
	since := intervalStart(interval)
	params := &stripe.IssuingAuthorizationListParams{
		Card: stripe.String(cardID),
	}
	params.Limit = stripe.Int64(100)
	params.CreatedRange = &stripe.RangeQueryParams{GreaterThanOrEqual: since.Unix()}
	it := issauth.List(params)
	var total int64
	for it.Next() {
		a := it.IssuingAuthorization()
		if a.Approved {
			total += a.Amount
		}
	}
	return total, it.Err()
}

func intervalStart(interval string) time.Time {
	now := time.Now()
	switch interval {
	case "weekly":
		d := int(now.Weekday())
		return time.Date(now.Year(), now.Month(), now.Day()-d, 0, 0, 0, 0, now.Location())
	case "monthly":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	default: // daily
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	}
}

func joinMerchants(m []string) string {
	out := ""
	for i, s := range m {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

// CreateWebhookEndpoint registers a new webhook endpoint with Stripe for the
// issuing authorization events. Returns the created endpoint including its
// signing secret (only available immediately after creation).
func CreateWebhookEndpoint(url string) (*stripe.WebhookEndpoint, error) {
	params := &stripe.WebhookEndpointParams{
		URL: stripe.String(url),
		EnabledEvents: stripe.StringSlice([]string{
			"issuing_authorization.request",
			"issuing_authorization.created",
			"issuing_authorization.updated",
		}),
		Description: stripe.String("agentdesk daemon"),
	}
	return whendpoint.New(params)
}

// FormatAuthorizationStatus returns a human-readable status for an authorization.
func FormatAuthorizationStatus(a *stripe.IssuingAuthorization) string {
	switch a.Status {
	case stripe.IssuingAuthorizationStatusPending:
		return "pending"
	case stripe.IssuingAuthorizationStatusClosed:
		if a.Approved {
			return "approved"
		}
		return "declined"
	case stripe.IssuingAuthorizationStatusReversed:
		return "reversed"
	case stripe.IssuingAuthorizationStatusExpired:
		return "expired"
	}
	return fmt.Sprintf("%v", a.Status)
}
