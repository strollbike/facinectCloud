package booking

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const paymentHoldTTL = 30 * time.Minute

type CreatePaymentHoldInput struct {
	SlotIDs       []int64  `json:"slot_ids"`
	WhatsApp      string   `json:"whatsapp"`
	CustomerName  string   `json:"customer_name"`
	Category      string   `json:"category"`
	SportName     string   `json:"sport_name"`
	LogoURL       string   `json:"logo_url"`
	FullAmount    float64  `json:"full_amount"`
	PayAmount     float64  `json:"pay_amount"` // advance or full to charge now
	AmountType    string   `json:"amount_type"` // advance | full
	CreatedBy     string   `json:"created_by"`
	PublicBaseURL string   `json:"public_base_url"` // e.g. http://192.168.1.11:8080
}

type PaymentHold struct {
	ID            int64     `json:"id"`
	FacilityID    int64     `json:"facility_id"`
	PublicToken   string    `json:"public_token"`
	WhatsApp      string    `json:"whatsapp"`
	CustomerName  string    `json:"customer_name"`
	Category      string    `json:"category"`
	SportName     string    `json:"sport_name"`
	LogoURL       string    `json:"logo_url"`
	SlotIDs       []int64   `json:"slot_ids"`
	SlotsSummary  string    `json:"slots_summary"`
	FullAmount    float64   `json:"full_amount"`
	PayAmount     float64   `json:"pay_amount"`
	AmountType    string    `json:"amount_type"`
	PaymentURL     string  `json:"payment_url"`
	ProviderLinkID string  `json:"provider_link_id,omitempty"`
	Status         string  `json:"status"`
	ExpiresAt      string  `json:"expires_at"`
	PaidAt         string  `json:"paid_at,omitempty"`
	HoldMinutes    int     `json:"hold_minutes"`
	BookingIDs     []int64 `json:"booking_ids,omitempty"`
	// App / facility user created or updated from WhatsApp + name.
	UserID int64 `json:"user_id,omitempty"`
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func normalizeWhatsApp(raw string) string {
	digits := strings.Builder{}
	for _, r := range strings.TrimSpace(raw) {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	s := digits.String()
	if len(s) == 10 {
		return "91" + s
	}
	return s
}

// CreatePaymentHold marks Free slots as held for 30 minutes and returns a pay URL.
func (s *Service) CreatePaymentHold(ctx context.Context, facilityID int64, in CreatePaymentHoldInput) (*PaymentHold, error) {
	if facilityID <= 0 || len(in.SlotIDs) == 0 {
		return nil, fmt.Errorf("invalid_hold")
	}
	wa := normalizeWhatsApp(in.WhatsApp)
	if len(wa) < 10 {
		return nil, fmt.Errorf("invalid_whatsapp")
	}
	amountType := strings.ToLower(strings.TrimSpace(in.AmountType))
	if amountType != "full" {
		amountType = "advance"
	}
	payAmt := in.PayAmount
	if payAmt <= 0 {
		payAmt = in.FullAmount
		amountType = "full"
	}
	if in.FullAmount < 0 || payAmt < 0 {
		return nil, fmt.Errorf("invalid_amount")
	}
	if payAmt > in.FullAmount && in.FullAmount > 0 {
		return nil, fmt.Errorf("pay_exceeds_full")
	}

	token, err := randomToken(16)
	if err != nil {
		return nil, err
	}
	expires := time.Now().Add(paymentHoldTTL)
	base := strings.TrimRight(strings.TrimSpace(in.PublicBaseURL), "/")
	if base == "" {
		base = "http://127.0.0.1:8080"
	}
	payURL := fmt.Sprintf("%s/v1/booking/pay/%s", base, token)

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Lock + validate all slots are available for this facility.
	summaryParts := make([]string, 0, len(in.SlotIDs))
	for _, sid := range in.SlotIDs {
		var fid int64
		var status, court, date, start, end string
		err := tx.QueryRowContext(ctx, `
SELECT s.facility_id, s.status, c.name, s.slot_date::text, s.start_time::text, s.end_time::text
FROM booking_slots s
JOIN booking_courts c ON c.id = s.court_id
WHERE s.id=$1 FOR UPDATE`, sid).Scan(&fid, &status, &court, &date, &start, &end)
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("slot_not_found")
		}
		if err != nil {
			return nil, err
		}
		if fid != facilityID {
			return nil, fmt.Errorf("facility_mismatch")
		}
		if status != "available" {
			return nil, fmt.Errorf("slot_not_available")
		}
		summaryParts = append(summaryParts, fmt.Sprintf("%s %s %s-%s", court, date, trimClock(start), trimClock(end)))
	}

	note := strings.TrimSpace(in.Category)
	if note == "" {
		note = "Payment hold"
	}
	note = "HOLD: " + note
	for _, sid := range in.SlotIDs {
		_, err := tx.ExecContext(ctx, `
UPDATE booking_slots SET status='held', notes=$2, updated_at=NOW() WHERE id=$1`, sid, note)
		if err != nil {
			return nil, err
		}
	}

	summary := strings.Join(summaryParts, "; ")
	var id int64
	err = tx.QueryRowContext(ctx, `
INSERT INTO booking_payment_holds (
  facility_id, public_token, whatsapp, customer_name, category, sport_name, logo_url,
  slot_ids, slots_summary, full_amount, pay_amount, amount_type, payment_url, status, expires_at, created_by
) VALUES (
  $1,$2,$3,NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),
  $8::bigint[],$9,$10,$11,$12,$13,'pending',$14,NULLIF($15,'')
) RETURNING id`,
		facilityID, token, wa, strings.TrimSpace(in.CustomerName), strings.TrimSpace(in.Category),
		strings.TrimSpace(in.SportName), strings.TrimSpace(in.LogoURL),
		pgInt64ArrayLiteral(in.SlotIDs), summary, in.FullAmount, payAmt, amountType, payURL, expires,
		strings.TrimSpace(in.CreatedBy),
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	// Prefer real Razorpay (or stub) payment link for WhatsApp.
	callback := fmt.Sprintf("%s/v1/booking/pay/%s/confirm", base, token)
	if linkURL, linkID, err := s.createGatewayPaymentLink(ctx, facilityID, payAmt, amountType, in, token, callback, expires); err == nil && linkURL != "" {
		_, _ = s.DB.ExecContext(ctx, `
UPDATE booking_payment_holds
SET payment_url=$2, provider_link_id=NULLIF($3,''), updated_at=NOW()
WHERE id=$1`, id, linkURL, linkID)
	}

	// Auto upsert users table + facility-wise customer (name + WhatsApp).
	userID, uerr := s.ensureFacilityAppUser(ctx, facilityID, wa, in.CustomerName)
	if uerr != nil {
		// Hold already created — surface user upsert failure without rolling back hold.
		_ = uerr
	}

	h, err := s.GetPaymentHold(ctx, facilityID, id)
	if err != nil {
		return nil, err
	}
	if userID > 0 {
		h.UserID = userID
	}
	return h, nil
}

func (s *Service) createGatewayPaymentLink(
	ctx context.Context,
	facilityID int64,
	payAmt float64,
	amountType string,
	in CreatePaymentHoldInput,
	token, callback string,
	expires time.Time,
) (shortURL, linkID string, err error) {
	baseGW := strings.TrimRight(strings.TrimSpace(s.Cfg.PaygatewayURL), "/")
	if baseGW == "" {
		return "", "", fmt.Errorf("paygateway_unconfigured")
	}
	desc := strings.TrimSpace(in.Category)
	if desc == "" {
		desc = "Booking"
	}
	if sn := strings.TrimSpace(in.SportName); sn != "" {
		desc = sn + " · " + desc
	}
	desc = fmt.Sprintf("%s (%s)", desc, amountType)
	body := map[string]interface{}{
		"amount":       payAmt,
		"currency":     "INR",
		"description":  desc,
		"callback_url": callback,
		"expire_by":    expires.Unix(),
		"customer": map[string]string{
			"name":    strings.TrimSpace(in.CustomerName),
			"contact": normalizeWhatsApp(in.WhatsApp),
		},
		"metadata": map[string]interface{}{
			"hold_token":  token,
			"facility_id": facilityID,
			"amount_type": amountType,
		},
		"facility_id": facilityID,
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseGW+"/v1/gateway/payment-links", bytes.NewReader(raw))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(s.Cfg.PaygatewayServiceKey); key != "" {
		req.Header.Set("X-Service-Key", key)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return "", "", fmt.Errorf("paygateway_%d: %s", res.StatusCode, string(respBody))
	}
	var out struct {
		ShortURL string `json:"short_url"`
		LinkID   string `json:"link_id"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", "", err
	}
	if out.ShortURL == "" {
		return "", "", fmt.Errorf("empty_short_url")
	}
	return out.ShortURL, out.LinkID, nil
}

func trimClock(t string) string {
	t = strings.TrimSpace(t)
	if len(t) >= 5 {
		return t[:5]
	}
	return t
}

func pgInt64ArrayLiteral(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("%d", id))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func (s *Service) GetPaymentHold(ctx context.Context, facilityID, holdID int64) (*PaymentHold, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, facility_id, public_token, whatsapp, COALESCE(customer_name,''), COALESCE(category,''),
       COALESCE(sport_name,''), COALESCE(logo_url,''), slot_ids::text, COALESCE(slots_summary,''),
       full_amount, pay_amount, amount_type, COALESCE(payment_url,''), COALESCE(provider_link_id,''), status,
       expires_at::text, COALESCE(paid_at::text,'')
FROM booking_payment_holds WHERE id=$1 AND facility_id=$2`, holdID, facilityID)
	return scanPaymentHold(row)
}

func (s *Service) GetPaymentHoldByToken(ctx context.Context, token string) (*PaymentHold, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("invalid_token")
	}
	row := s.DB.QueryRowContext(ctx, `
SELECT id, facility_id, public_token, whatsapp, COALESCE(customer_name,''), COALESCE(category,''),
       COALESCE(sport_name,''), COALESCE(logo_url,''), slot_ids::text, COALESCE(slots_summary,''),
       full_amount, pay_amount, amount_type, COALESCE(payment_url,''), COALESCE(provider_link_id,''), status,
       expires_at::text, COALESCE(paid_at::text,'')
FROM booking_payment_holds WHERE public_token=$1`, token)
	return scanPaymentHold(row)
}

func scanPaymentHold(row *sql.Row) (*PaymentHold, error) {
	var h PaymentHold
	var slotText string
	var full, pay float64
	err := row.Scan(
		&h.ID, &h.FacilityID, &h.PublicToken, &h.WhatsApp, &h.CustomerName, &h.Category,
		&h.SportName, &h.LogoURL, &slotText, &h.SlotsSummary,
		&full, &pay, &h.AmountType, &h.PaymentURL, &h.ProviderLinkID, &h.Status, &h.ExpiresAt, &h.PaidAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("not_found")
	}
	if err != nil {
		return nil, err
	}
	h.FullAmount = full
	h.PayAmount = pay
	h.SlotIDs = parsePgInt64Array(slotText)
	h.HoldMinutes = int(paymentHoldTTL / time.Minute)
	// Normalize absolute timestamps to UTC ISO-8601 for both apps.
	if t := ParseHoldExpiresAt(h.ExpiresAt); !t.IsZero() {
		h.ExpiresAt = t.UTC().Format(time.RFC3339)
	}
	if t := ParseHoldExpiresAt(h.PaidAt); !t.IsZero() {
		h.PaidAt = t.UTC().Format(time.RFC3339)
	}
	return &h, nil
}

func parsePgInt64Array(s string) []int64 {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	if s == "" {
		return nil
	}
	var out []int64
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var n int64
		_, _ = fmt.Sscan(p, &n)
		if n > 0 {
			out = append(out, n)
		}
	}
	return out
}

// ConfirmPaymentHold books held slots and marks the hold paid (webhook / admin / pay page).
func (s *Service) ConfirmPaymentHold(ctx context.Context, token string) (*PaymentHold, error) {
	h, err := s.GetPaymentHoldByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if h.Status == "paid" {
		return h, nil
	}
	if h.Status != "pending" {
		return nil, fmt.Errorf("hold_not_pending")
	}
	if paymentHoldPastExpiry(h) {
		_, _ = s.ExpirePaymentHold(ctx, h.ID)
		return nil, fmt.Errorf("hold_expired")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Ensure customer exists facility-wise before writing bookings.
	var userID interface{}
	if uid, uerr := s.ensureFacilityAppUser(ctx, h.FacilityID, h.WhatsApp, h.CustomerName); uerr == nil && uid > 0 {
		userID = uid
	}

	bookingIDs := make([]int64, 0, len(h.SlotIDs))
	notes := strings.TrimSpace(h.Category)
	if notes == "" {
		notes = "Paid booking"
	}
	for _, sid := range h.SlotIDs {
		var status string
		err := tx.QueryRowContext(ctx, `
SELECT status FROM booking_slots WHERE id=$1 AND facility_id=$2 FOR UPDATE`,
			sid, h.FacilityID).Scan(&status)
		if err != nil {
			return nil, err
		}
		if status != "held" && status != "available" {
			return nil, fmt.Errorf("slot_not_holdable")
		}
		var bookingID int64
		err = tx.QueryRowContext(ctx, `
INSERT INTO booking_bookings (
  facility_id, slot_id, user_id, customer_name, customer_phone, status, notes, payment_status, pay_mode, amount_paid
) VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),'confirmed',NULLIF($6,''),'paid',$7,$8)
RETURNING id`,
			h.FacilityID, sid, userID, h.CustomerName, h.WhatsApp, notes, h.AmountType, h.PayAmount,
		).Scan(&bookingID)
		if err != nil {
			return nil, err
		}
		bookingIDs = append(bookingIDs, bookingID)
		_, err = tx.ExecContext(ctx, `
UPDATE booking_slots SET status='booked', notes=$2, updated_at=NOW() WHERE id=$1`, sid, notes)
		if err != nil {
			return nil, err
		}
	}

	_, err = tx.ExecContext(ctx, `
UPDATE booking_payment_holds SET status='paid', paid_at=NOW(), updated_at=NOW() WHERE id=$1`, h.ID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for _, sid := range h.SlotIDs {
		_ = s.syncAppSlotFromBookingSlot(ctx, sid)
	}
	out, err := s.GetPaymentHoldByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	out.BookingIDs = bookingIDs
	for _, id := range bookingIDs {
		if booked, berr := s.GetBooking(ctx, id); berr == nil {
			s.notifyFacilityBooking(ctx, booked, false)
		}
	}
	return out, nil
}

func ParseHoldExpiresAt(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05-07",
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

func paymentHoldPastExpiry(h *PaymentHold) bool {
	if h == nil || h.Status != "pending" {
		return false
	}
	exp := ParseHoldExpiresAt(h.ExpiresAt)
	return !exp.IsZero() && time.Now().After(exp)
}

func (s *Service) ExpirePaymentHold(ctx context.Context, holdID int64) (int, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var slotText, status, linkID string
	err = tx.QueryRowContext(ctx, `
SELECT slot_ids::text, status, COALESCE(provider_link_id,'')
FROM booking_payment_holds WHERE id=$1 FOR UPDATE`, holdID).
		Scan(&slotText, &status, &linkID)
	if err != nil {
		return 0, err
	}
	if status != "pending" {
		return 0, nil
	}
	ids := parsePgInt64Array(slotText)
	freed := 0
	for _, sid := range ids {
		res, err := tx.ExecContext(ctx, `
UPDATE booking_slots SET status='available', notes=NULL, updated_at=NOW()
WHERE id=$1 AND status='held'`, sid)
		if err != nil {
			return freed, err
		}
		n, _ := res.RowsAffected()
		freed += int(n)
	}
	_, err = tx.ExecContext(ctx, `
UPDATE booking_payment_holds SET status='expired', updated_at=NOW() WHERE id=$1`, holdID)
	if err != nil {
		return freed, err
	}
	if err := tx.Commit(); err != nil {
		return freed, err
	}
	// Best-effort: cancel Razorpay (or stub) link so rzp.io URL dies immediately.
	if linkID != "" {
		_ = s.cancelGatewayPaymentLink(ctx, linkID)
	}
	return freed, nil
}

func (s *Service) cancelGatewayPaymentLink(ctx context.Context, linkID string) error {
	linkID = strings.TrimSpace(linkID)
	baseGW := strings.TrimRight(strings.TrimSpace(s.Cfg.PaygatewayURL), "/")
	if linkID == "" || baseGW == "" {
		return fmt.Errorf("cancel_unconfigured")
	}
	url := baseGW + "/v1/gateway/payment-links/" + linkID + "/cancel"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	if key := strings.TrimSpace(s.Cfg.PaygatewayServiceKey); key != "" {
		req.Header.Set("X-Service-Key", key)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	if res.StatusCode >= 300 {
		return fmt.Errorf("paygateway_cancel_%d", res.StatusCode)
	}
	return nil
}

// ExpireDuePaymentHolds releases pending holds past expires_at (cron).
func (s *Service) ExpireDuePaymentHolds(ctx context.Context) (int, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id FROM booking_payment_holds
WHERE status='pending' AND expires_at < NOW()`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	freed := 0
	for _, id := range ids {
		n, err := s.ExpirePaymentHold(ctx, id)
		if err != nil {
			continue
		}
		freed += n
	}
	return freed, nil
}
