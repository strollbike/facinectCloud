package tournaments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CheckoutInput selects full / advance / spot for an existing entry.
type CheckoutInput struct {
	PaymentMode string `json:"payment_mode"`
}

type CheckoutResult struct {
	EntryID     int64                  `json:"entry_id"`
	PaymentMode string                 `json:"payment_mode"`
	Amount      float64                `json:"amount"`
	Currency    string                 `json:"currency"`
	OrderID     string                 `json:"order_id,omitempty"`
	KeyID       string                 `json:"key_id,omitempty"`
	Provider    string                 `json:"provider,omitempty"`
	Checkout    map[string]interface{} `json:"checkout,omitempty"`
	Entry       *Entry                 `json:"entry,omitempty"`
	Spot        bool                   `json:"spot,omitempty"`
}

type ConfirmPaymentInput struct {
	OrderID   string `json:"order_id"`
	PaymentID string `json:"payment_id"`
	Signature string `json:"signature"`
}

type GatewayPaidEvent struct {
	OrderID   string  `json:"order_id"`
	PaymentID string  `json:"payment_id"`
	Amount    float64 `json:"amount"`
	Purpose   string  `json:"purpose"`
	EntryID   int64   `json:"entry_id"`
}

func normalizePaymentMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "full", "advance", "spot":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return ""
	}
}

func (s *Service) categoryFeeForEntry(ctx context.Context, tournamentID int64, category string) (float64, error) {
	cats, err := s.ListCategories(ctx, tournamentID)
	if err != nil {
		return 0, err
	}
	cat := strings.TrimSpace(category)
	for _, c := range cats {
		if strings.EqualFold(c.Name, cat) {
			if c.Fee > 0 {
				return c.Fee, nil
			}
			return c.EntryFee, nil
		}
	}
	if len(cats) == 0 {
		return 0, nil
	}
	return 0, fmt.Errorf("unknown_category")
}

func (s *Service) resolveCheckoutAmount(tour *Tournament, fee float64, mode string) (float64, error) {
	switch mode {
	case "full":
		if !tour.AllowFullPayment {
			return 0, fmt.Errorf("full_payment_disabled")
		}
		if fee < 0 {
			fee = 0
		}
		return fee, nil
	case "advance":
		if !tour.AllowAdvancePayment {
			return 0, fmt.Errorf("advance_payment_disabled")
		}
		amt := tour.AdvanceAmount
		if amt <= 0 {
			return 0, fmt.Errorf("advance_amount_missing")
		}
		if fee > 0 && amt > fee {
			amt = fee
		}
		return amt, nil
	case "spot":
		if !tour.AllowSpotPayment {
			return 0, fmt.Errorf("spot_payment_disabled")
		}
		return 0, nil
	default:
		return 0, fmt.Errorf("invalid_payment_mode")
	}
}

// CreateEntryCheckout creates a paygateway order (or marks spot). Never trusts client paid.
func (s *Service) CreateEntryCheckout(ctx context.Context, entryID int64, in CheckoutInput) (*CheckoutResult, error) {
	entry, err := s.getEntry(ctx, entryID)
	if err != nil {
		return nil, fmt.Errorf("not_found")
	}
	if isPaidTournamentStatus(entry.PaymentStatus) && !strings.EqualFold(entry.PaymentStatus, "spot") {
		return &CheckoutResult{
			EntryID: entry.ID, PaymentMode: entry.PaymentMode, Amount: entry.PaidAmount,
			Currency: "INR", Entry: entry,
		}, nil
	}
	tour, err := s.Get(ctx, entry.TournamentID)
	if err != nil {
		return nil, err
	}
	mode := normalizePaymentMode(in.PaymentMode)
	if mode == "" {
		mode = normalizePaymentMode(entry.PaymentMode)
	}
	if mode == "" {
		if tour.AllowFullPayment {
			mode = "full"
		} else if tour.AllowAdvancePayment {
			mode = "advance"
		} else if tour.AllowSpotPayment {
			mode = "spot"
		} else {
			return nil, fmt.Errorf("payment_mode_required")
		}
	}
	fee, err := s.categoryFeeForEntry(ctx, tour.ID, entry.Category)
	if err != nil {
		return nil, err
	}
	amount, err := s.resolveCheckoutAmount(tour, fee, mode)
	if err != nil {
		return nil, err
	}

	if mode == "spot" {
		txn := fmt.Sprintf("SPOT_%d", entry.ID)
		_, err := s.DB.ExecContext(ctx, `
UPDATE tournament_entries SET
  payment_status='spot', payment_mode='spot', paid_amount=0,
  transaction_id=COALESCE(NULLIF(transaction_id,''), $1)
WHERE id=$2`, txn, entry.ID)
		if err != nil {
			return nil, err
		}
		updated, err := s.getEntry(ctx, entry.ID)
		if err != nil {
			return nil, err
		}
		s.notifyTournamentRegistrationConfirmation(ctx, updated, 0, "spot")
		return &CheckoutResult{
			EntryID: entry.ID, PaymentMode: "spot", Amount: 0, Currency: "INR",
			Entry: updated, Spot: true,
		}, nil
	}

	orderID, keyID, provider, checkout, err := s.createPaygatewayOrder(ctx, tour, entry, mode, amount)
	if err != nil {
		return nil, err
	}
	_, err = s.DB.ExecContext(ctx, `
UPDATE tournament_entries SET
  payment_mode=$1, paid_amount=$2, gateway_order_id=$3, payment_status='pending'
WHERE id=$4`, mode, amount, orderID, entry.ID)
	if err != nil {
		return nil, err
	}
	updated, err := s.getEntry(ctx, entry.ID)
	if err != nil {
		return nil, err
	}
	return &CheckoutResult{
		EntryID: entry.ID, PaymentMode: mode, Amount: amount, Currency: "INR",
		OrderID: orderID, KeyID: keyID, Provider: provider, Checkout: checkout, Entry: updated,
	}, nil
}

func (s *Service) createPaygatewayOrder(
	ctx context.Context, tour *Tournament, entry *Entry, mode string, amount float64,
) (orderID, keyID, provider string, checkout map[string]interface{}, err error) {
	base := strings.TrimRight(strings.TrimSpace(s.Cfg.PaygatewayURL), "/")
	if base == "" {
		return "", "", "", nil, fmt.Errorf("paygateway_unconfigured")
	}
	paise := int64(amount*100 + 0.5)
	if paise < 100 && amount > 0 {
		paise = 100
	}
	body := map[string]interface{}{
		"amount":       amount,
		"amount_paise": paise,
		"currency":     "INR",
		"receipt":      fmt.Sprintf("tour_%d_entry_%d", tour.ID, entry.ID),
		"purpose":      "tournament",
		"facility_id":  tour.FacilityID,
		"customer": map[string]string{
			"name":  entry.PlayerName,
			"phone": entry.Phone,
			"email": entry.Email,
		},
		"metadata": map[string]interface{}{
			"purpose":       "tournament",
			"entry_id":      entry.ID,
			"tournament_id": tour.ID,
			"facility_id":   tour.FacilityID,
			"payment_mode":  mode,
			"category":      entry.Category,
		},
		"notes": map[string]interface{}{
			"purpose":       "tournament",
			"entry_id":      entry.ID,
			"tournament_id": tour.ID,
			"payment_mode":  mode,
		},
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/gateway/orders", bytes.NewReader(raw))
	if err != nil {
		return "", "", "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(s.Cfg.PaygatewayKey); key != "" {
		req.Header.Set("X-Service-Key", key)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("paygateway_unreachable")
	}
	defer res.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return "", "", "", nil, fmt.Errorf("paygateway_%d: %s", res.StatusCode, string(respBody))
	}
	var gw struct {
		Provider        string                 `json:"provider"`
		ExternalOrderID string                 `json:"external_order_id"`
		OrderID         string                 `json:"order_id"`
		Checkout        map[string]interface{} `json:"checkout"`
	}
	if err := json.Unmarshal(respBody, &gw); err != nil {
		return "", "", "", nil, fmt.Errorf("paygateway_bad_json")
	}
	orderID = strings.TrimSpace(gw.ExternalOrderID)
	if orderID == "" {
		orderID = strings.TrimSpace(gw.OrderID)
	}
	if gw.Checkout != nil {
		if v, ok := gw.Checkout["order_id"].(string); ok && strings.TrimSpace(v) != "" {
			orderID = strings.TrimSpace(v)
		}
		if v, ok := gw.Checkout["key"].(string); ok {
			keyID = v
		}
	}
	if orderID == "" {
		return "", "", "", nil, fmt.Errorf("order_id_missing")
	}
	return orderID, keyID, gw.Provider, gw.Checkout, nil
}

// ConfirmEntryPayment verifies signature with paygateway then marks paid (idempotent).
func (s *Service) ConfirmEntryPayment(ctx context.Context, entryID int64, in ConfirmPaymentInput) (*Entry, error) {
	entry, err := s.getEntry(ctx, entryID)
	if err != nil {
		return nil, fmt.Errorf("not_found")
	}
	if isPaidTournamentStatus(entry.PaymentStatus) && !strings.EqualFold(entry.PaymentStatus, "spot") {
		return entry, nil
	}
	orderID := strings.TrimSpace(in.OrderID)
	if orderID == "" {
		orderID = strings.TrimSpace(entry.GatewayOrderID)
	}
	paymentID := strings.TrimSpace(in.PaymentID)
	if orderID == "" || paymentID == "" {
		return nil, fmt.Errorf("order_and_payment_required")
	}
	if err := s.verifyPaygatewayPayment(ctx, orderID, paymentID, strings.TrimSpace(in.Signature)); err != nil {
		return nil, err
	}
	return s.markEntryPaidFromGateway(ctx, entry, paymentID, orderID, entry.PaidAmount)
}

func (s *Service) verifyPaygatewayPayment(ctx context.Context, orderID, paymentID, signature string) error {
	base := strings.TrimRight(strings.TrimSpace(s.Cfg.PaygatewayURL), "/")
	if base == "" {
		return fmt.Errorf("paygateway_unconfigured")
	}
	body := map[string]interface{}{
		"external_order_id": orderID,
		"payment_id":        paymentID,
		"signature":         signature,
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/gateway/verify", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(s.Cfg.PaygatewayKey); key != "" {
		req.Header.Set("X-Service-Key", key)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("paygateway_unreachable")
	}
	defer res.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var vr struct {
		Valid  bool   `json:"valid"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(respBody, &vr)
	if res.StatusCode >= 300 || !vr.Valid {
		msg := strings.TrimSpace(vr.Reason)
		if msg == "" {
			msg = "payment_verify_failed"
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// ApplyGatewayPaidEvent is the webhook/fan-out path (purpose=tournament only).
func (s *Service) ApplyGatewayPaidEvent(ctx context.Context, ev GatewayPaidEvent) (*Entry, error) {
	purpose := strings.ToLower(strings.TrimSpace(ev.Purpose))
	if purpose != "" && purpose != "tournament" && purpose != "tournaments" {
		return nil, fmt.Errorf("purpose_mismatch")
	}
	paymentID := strings.TrimSpace(ev.PaymentID)
	orderID := strings.TrimSpace(ev.OrderID)
	if paymentID == "" && orderID == "" {
		return nil, fmt.Errorf("payment_or_order_required")
	}

	var entry *Entry
	var err error
	if ev.EntryID > 0 {
		entry, err = s.getEntry(ctx, ev.EntryID)
	} else if orderID != "" {
		entry, err = s.getEntryByGatewayOrder(ctx, orderID)
	} else {
		entry, err = s.getEntryByTransaction(ctx, paymentID)
	}
	if err != nil {
		return nil, fmt.Errorf("not_found")
	}
	if isPaidTournamentStatus(entry.PaymentStatus) && !strings.EqualFold(entry.PaymentStatus, "spot") {
		return entry, nil
	}
	amt := ev.Amount
	if amt <= 0 {
		amt = entry.PaidAmount
	}
	return s.markEntryPaidFromGateway(ctx, entry, paymentID, orderID, amt)
}

func (s *Service) getEntryByGatewayOrder(ctx context.Context, orderID string) (*Entry, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `
SELECT id FROM tournament_entries WHERE gateway_order_id=$1 ORDER BY id DESC LIMIT 1`, orderID).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.getEntry(ctx, id)
}

func (s *Service) getEntryByTransaction(ctx context.Context, txn string) (*Entry, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `
SELECT id FROM tournament_entries WHERE transaction_id=$1 ORDER BY id DESC LIMIT 1`, txn).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.getEntry(ctx, id)
}

func (s *Service) markEntryPaidFromGateway(ctx context.Context, entry *Entry, paymentID, orderID string, amount float64) (*Entry, error) {
	status := "paid"
	mode := strings.TrimSpace(entry.PaymentMode)
	if mode == "" {
		mode = "full"
	}
	// Advance below full fee stays pending (PHP parity).
	if mode == "advance" {
		fee, _ := s.categoryFeeForEntry(ctx, entry.TournamentID, entry.Category)
		if fee > 0 && amount+0.001 < fee {
			status = "pending"
		}
	}
	txn := strings.TrimSpace(paymentID)
	if txn == "" {
		txn = strings.TrimSpace(entry.TransactionID)
	}
	res, err := s.DB.ExecContext(ctx, `
UPDATE tournament_entries SET
  payment_status=$1,
  payment_mode=COALESCE(NULLIF($2,''), payment_mode),
  paid_amount=CASE WHEN $3::float8 > 0 THEN $3::float8 ELSE paid_amount END,
  transaction_id=COALESCE(NULLIF($4,''), transaction_id),
  gateway_order_id=COALESCE(NULLIF($5,''), gateway_order_id)
WHERE id=$6
  AND (
    LOWER(payment_status) NOT IN ('paid','completed','success')
    OR COALESCE(transaction_id,'') = ''
    OR COALESCE(transaction_id,'') = $4
  )`, status, mode, amount, txn, orderID, entry.ID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "idx_tournament_entries_txn") {
			existing, e2 := s.getEntryByTransaction(ctx, txn)
			if e2 == nil {
				return existing, nil
			}
		}
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return s.getEntry(ctx, entry.ID)
	}
	updated, err := s.getEntry(ctx, entry.ID)
	if err != nil {
		return nil, err
	}
	wasPaid := isPaidTournamentStatus(entry.PaymentStatus) && !strings.EqualFold(entry.PaymentStatus, "spot")
	if isPaidTournamentStatus(updated.PaymentStatus) && !wasPaid {
		s.notifyTournamentRegistrationConfirmation(ctx, updated, amount, mode)
	}
	return updated, nil
}
