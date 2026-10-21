package razorpay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/facinect/paygateway/internal/gateway"
)

type Provider struct {
	KeyID         string
	KeySecret     string
	WebhookSecret string
	HTTP          *http.Client
}

func New(keyID, keySecret, webhookSecret string) *Provider {
	return &Provider{
		KeyID:         strings.TrimSpace(keyID),
		KeySecret:     strings.TrimSpace(keySecret),
		WebhookSecret: strings.TrimSpace(webhookSecret),
		HTTP:          &http.Client{Timeout: 15 * time.Second},
	}
}

func (p *Provider) Name() string { return "razorpay" }

func (p *Provider) Configured() bool {
	return p.KeyID != "" && p.KeySecret != ""
}

func (p *Provider) CreateOrder(ctx context.Context, req gateway.CreateRequest) (*gateway.CreateResult, error) {
	if !p.Configured() {
		return nil, fmt.Errorf("razorpay_not_configured")
	}
	paise := req.AmountPaise
	if paise <= 0 && req.Amount > 0 {
		paise = int64(req.Amount*100 + 0.5)
	}
	if paise <= 0 {
		return nil, fmt.Errorf("invalid_amount")
	}
	cur := strings.ToUpper(strings.TrimSpace(req.Currency))
	if cur == "" {
		cur = "INR"
	}
	body := map[string]interface{}{
		"amount":   paise,
		"currency": cur,
	}
	if req.Receipt != "" {
		body["receipt"] = req.Receipt
	}
	notes := map[string]interface{}{}
	for k, v := range req.Metadata {
		notes[k] = v
	}
	if req.Purpose != "" {
		notes["purpose"] = req.Purpose
	}
	if req.FacilityID != nil {
		notes["facility_id"] = *req.FacilityID
	}
	if len(notes) > 0 {
		body["notes"] = notes
	}
	raw, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.razorpay.com/v1/orders", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.SetBasicAuth(p.KeyID, p.KeySecret)
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := p.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("razorpay_http_%d: %s", res.StatusCode, truncate(string(respBody), 200))
	}
	var out struct {
		ID       string `json:"id"`
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
		Receipt  string `json:"receipt"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, err
	}
	return &gateway.CreateResult{
		Provider:        "razorpay",
		ExternalOrderID: out.ID,
		Status:          "created",
		AmountPaise:     out.Amount,
		Currency:        out.Currency,
		Receipt:         out.Receipt,
		Checkout: map[string]interface{}{
			"key":      p.KeyID,
			"order_id": out.ID,
			"amount":   out.Amount,
			"currency": out.Currency,
			"name":     "Facinect",
			"prefill": map[string]string{
				"name":    req.Customer.Name,
				"email":   req.Customer.Email,
				"contact": req.Customer.Phone,
			},
		},
	}, nil
}

func (p *Provider) CreatePaymentLink(ctx context.Context, req gateway.PaymentLinkRequest) (*gateway.PaymentLinkResult, error) {
	if !p.Configured() {
		return nil, fmt.Errorf("razorpay_not_configured")
	}
	paise := req.AmountPaise
	if paise <= 0 && req.Amount > 0 {
		paise = int64(req.Amount*100 + 0.5)
	}
	if paise <= 0 {
		return nil, fmt.Errorf("invalid_amount")
	}
	cur := strings.ToUpper(strings.TrimSpace(req.Currency))
	if cur == "" {
		cur = "INR"
	}
	desc := strings.TrimSpace(req.Description)
	if desc == "" {
		desc = "Facinect booking payment"
	}
	body := map[string]interface{}{
		"amount":          paise,
		"currency":        cur,
		"accept_partial":  false,
		"description":     desc,
		"notify":          map[string]bool{"sms": false, "email": false},
		"reminder_enable": false,
	}
	cust := map[string]string{}
	if n := strings.TrimSpace(req.Customer.Name); n != "" {
		cust["name"] = n
	}
	if e := strings.TrimSpace(req.Customer.Email); e != "" {
		cust["email"] = e
	}
	if ph := strings.TrimSpace(req.Customer.Phone); ph != "" {
		cust["contact"] = ph
	}
	if len(cust) > 0 {
		body["customer"] = cust
	}
	if cb := strings.TrimSpace(req.CallbackURL); cb != "" {
		body["callback_url"] = cb
		body["callback_method"] = "get"
	}
	if req.ExpireBy > 0 {
		body["expire_by"] = req.ExpireBy
	}
	notes := map[string]interface{}{}
	for k, v := range req.Metadata {
		notes[k] = v
	}
	if req.FacilityID != nil {
		notes["facility_id"] = *req.FacilityID
	}
	if len(notes) > 0 {
		body["notes"] = notes
	}
	raw, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.razorpay.com/v1/payment_links", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.SetBasicAuth(p.KeyID, p.KeySecret)
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := p.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("razorpay_http_%d: %s", res.StatusCode, truncate(string(respBody), 300))
	}
	var out struct {
		ID       string `json:"id"`
		ShortURL string `json:"short_url"`
		Status   string `json:"status"`
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, err
	}
	if out.ShortURL == "" {
		return nil, fmt.Errorf("razorpay_empty_short_url")
	}
	return &gateway.PaymentLinkResult{
		Provider:    "razorpay",
		LinkID:      out.ID,
		ShortURL:    out.ShortURL,
		Status:      out.Status,
		AmountPaise: out.Amount,
		Currency:    out.Currency,
	}, nil
}

func (p *Provider) CancelPaymentLink(ctx context.Context, linkID string) error {
	linkID = strings.TrimSpace(linkID)
	if !p.Configured() {
		return fmt.Errorf("razorpay_not_configured")
	}
	if linkID == "" {
		return fmt.Errorf("invalid_link_id")
	}
	url := "https://api.razorpay.com/v1/payment_links/" + linkID + "/cancel"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	httpReq.SetBasicAuth(p.KeyID, p.KeySecret)
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := p.HTTP.Do(httpReq)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	// 200 OK, or already cancelled/expired — treat as success for idempotency.
	if res.StatusCode >= 300 {
		body := strings.ToLower(string(respBody))
		if strings.Contains(body, "already") || strings.Contains(body, "cancelled") || strings.Contains(body, "expired") {
			return nil
		}
		return fmt.Errorf("razorpay_cancel_http_%d: %s", res.StatusCode, truncate(string(respBody), 300))
	}
	return nil
}

func (p *Provider) Verify(ctx context.Context, req gateway.VerifyRequest) (*gateway.VerifyResult, error) {
	_ = ctx
	if !p.Configured() {
		return nil, fmt.Errorf("razorpay_not_configured")
	}
	if req.ExternalOrderID == "" || req.PaymentID == "" || req.Signature == "" {
		return &gateway.VerifyResult{Provider: "razorpay", Valid: false, Reason: "missing_fields"}, nil
	}
	mac := hmac.New(sha256.New, []byte(p.KeySecret))
	mac.Write([]byte(req.ExternalOrderID + "|" + req.PaymentID))
	expected := hex.EncodeToString(mac.Sum(nil))
	ok := hmac.Equal([]byte(expected), []byte(req.Signature))
	st := "failed"
	if ok {
		st = "paid"
	}
	return &gateway.VerifyResult{
		Provider:        "razorpay",
		Valid:           ok,
		Status:          st,
		ExternalOrderID: req.ExternalOrderID,
		PaymentID:       req.PaymentID,
		Reason:          map[bool]string{true: "", false: "invalid_signature"}[ok],
	}, nil
}

func (p *Provider) ParseWebhook(headers http.Header, body []byte) (*gateway.NormalizedEvent, error) {
	return p.ParseWebhookSecret(headers, body, p.WebhookSecret)
}

func (p *Provider) ParseWebhookSecret(headers http.Header, body []byte, secret string) (*gateway.NormalizedEvent, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		secret = p.WebhookSecret
	}
	if secret != "" {
		sig := headers.Get("X-Razorpay-Signature")
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		expected := hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(expected), []byte(sig)) {
			return nil, fmt.Errorf("invalid_webhook_signature")
		}
	}
	var payload struct {
		Event   string `json:"event"`
		Payload struct {
			Payment struct {
				Entity map[string]interface{} `json:"entity"`
			} `json:"payment"`
			Order struct {
				Entity map[string]interface{} `json:"entity"`
			} `json:"order"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	ev := &gateway.NormalizedEvent{
		Provider: "razorpay",
		Type:     normalizeRazorpayEvent(payload.Event),
		Raw:      map[string]interface{}{"event": payload.Event},
	}
	ev.Purpose = purposeFromNotes(payload.Payload.Payment.Entity["notes"])
	if id, ok := payload.Payload.Payment.Entity["id"].(string); ok {
		ev.PaymentID = id
	}
	if oid, ok := payload.Payload.Payment.Entity["order_id"].(string); ok {
		ev.ExternalOrderID = oid
	}
	if oid, ok := payload.Payload.Order.Entity["id"].(string); ok && ev.ExternalOrderID == "" {
		ev.ExternalOrderID = oid
	}
	if amt, ok := asInt64(payload.Payload.Payment.Entity["amount"]); ok {
		ev.AmountPaise = amt
	}
	if cur, ok := payload.Payload.Payment.Entity["currency"].(string); ok {
		ev.Currency = cur
	}
	return ev, nil
}

func purposeFromNotes(raw interface{}) string {
	notes, ok := raw.(map[string]interface{})
	if !ok || notes == nil {
		return ""
	}
	if v, ok := notes["purpose"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v, ok := notes["category"].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func normalizeRazorpayEvent(e string) string {
	switch e {
	case "payment.captured", "order.paid":
		return "payment.captured"
	case "payment.failed":
		return "payment.failed"
	case "refund.created", "refund.processed":
		return "refunded"
	default:
		if e == "" {
			return "unknown"
		}
		return e
	}
}

func asInt64(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	default:
		return 0, false
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
