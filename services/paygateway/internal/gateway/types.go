package gateway

import "net/http"

// CreateRequest is the stable caller contract (saas-billing / booking).
// AmountPaise is the canonical unit (Razorpay-style); rupees callers may send Amount.
type CreateRequest struct {
	Provider  string                 `json:"provider"`
	Amount    float64                `json:"amount"`       // rupees (optional if amount_paise set)
	AmountPaise int64                `json:"amount_paise"` // preferred
	Currency  string                 `json:"currency"`
	Receipt   string                 `json:"receipt"`
	Customer  Customer               `json:"customer"`
	Metadata  map[string]interface{} `json:"metadata"`
	ReturnURL  string                 `json:"return_url"`
	FacilityID *int64                `json:"facility_id"`
	Purpose    string                 `json:"purpose"`
	Notes      map[string]interface{} `json:"notes"`
}

type Customer struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Phone string `json:"phone"`
}

type CreateResult struct {
	Provider        string                 `json:"provider"`
	OrderID         string                 `json:"order_id"`          // our internal id as string
	ExternalOrderID string                 `json:"external_order_id"` // provider order id
	Status          string                 `json:"status"`
	AmountPaise     int64                  `json:"amount_paise"`
	Currency        string                 `json:"currency"`
	Receipt         string                 `json:"receipt,omitempty"`
	Checkout        map[string]interface{} `json:"checkout"`
}

// PaymentLinkRequest creates a shareable Razorpay (or stub) payment URL for WhatsApp.
type PaymentLinkRequest struct {
	Provider    string                 `json:"provider"`
	Amount      float64                `json:"amount"`
	AmountPaise int64                  `json:"amount_paise"`
	Currency    string                 `json:"currency"`
	Description string                 `json:"description"`
	Customer    Customer               `json:"customer"`
	CallbackURL string                 `json:"callback_url"`
	Metadata    map[string]interface{} `json:"metadata"`
	FacilityID  *int64                 `json:"facility_id"`
	ExpireBy    int64                  `json:"expire_by"` // unix seconds; 0 = provider default
}

type PaymentLinkResult struct {
	Provider   string `json:"provider"`
	LinkID     string `json:"link_id"`
	ShortURL   string `json:"short_url"`
	Status     string `json:"status"`
	AmountPaise int64 `json:"amount_paise"`
	Currency   string `json:"currency"`
}

type VerifyRequest struct {
	Provider        string `json:"provider"`
	ExternalOrderID string `json:"external_order_id"`
	PaymentID       string `json:"payment_id"`
	Signature       string `json:"signature"`
}

type VerifyResult struct {
	Provider        string `json:"provider"`
	Valid           bool   `json:"valid"`
	Status          string `json:"status,omitempty"`
	ExternalOrderID string `json:"external_order_id,omitempty"`
	PaymentID       string `json:"payment_id,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

type NormalizedEvent struct {
	Provider        string                 `json:"provider"`
	Type            string                 `json:"type"` // payment.captured | payment.failed | refunded
	ExternalOrderID string                 `json:"external_order_id,omitempty"`
	PaymentID       string                 `json:"payment_id,omitempty"`
	AmountPaise     int64                  `json:"amount_paise,omitempty"`
	Currency        string                 `json:"currency,omitempty"`
	Purpose         string                 `json:"purpose,omitempty"`
	Raw             map[string]interface{} `json:"raw,omitempty"`
}

// WebhookResult is the stable response for a channel webhook.
// Ignored is still HTTP 200 so the provider does not retry the other channel.
type WebhookResult struct {
	Provider        string `json:"provider"`
	Type            string `json:"type"`
	ExternalOrderID string `json:"external_order_id,omitempty"`
	PaymentID       string `json:"payment_id,omitempty"`
	Purpose         string `json:"purpose,omitempty"`
	Channel         string `json:"channel"`
	Accepted        bool   `json:"accepted"`
	Ignored         bool   `json:"ignored"`
	Reason          string `json:"reason,omitempty"`
}

// WebhookSecretParser lets one provider verify two webhook secrets
// (customer payments vs facility subscription) without a second integration.
type WebhookSecretParser interface {
	ParseWebhookSecret(headers http.Header, body []byte, secret string) (*NormalizedEvent, error)
}

type ProviderInfo struct {
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
	IsDefault  bool   `json:"is_default"`
}
