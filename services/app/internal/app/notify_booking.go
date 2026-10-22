package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// notifyBookingConfirmation mirrors Facinect PHP notifyBookingConfirmation:
// inbox + FCM (booking_user_notify) and WhatsApp template booking_confirmation.
// Best-effort: never fails confirm_booking.
func (s *Service) notifyBookingConfirmation(
	ctx context.Context,
	userID, facilityID, bookingID int64,
	slots []any,
	paymentMode, transactionID string,
	paidAmount, discountTotal, totalBill, balanceDue float64,
) {
	if userID <= 0 || facilityID <= 0 || bookingID <= 0 || len(slots) == 0 {
		return
	}

	u, _ := s.userRow(ctx, userID, "")
	uName := "Customer"
	uPhone := ""
	if u != nil {
		if v := strings.TrimSpace(fmt.Sprint(u["fullName"])); v != "" && v != "<nil>" {
			uName = v
		}
		uPhone = normalizeWA(fmt.Sprint(u["whatsappNo"]))
		if uPhone == "" {
			uPhone = normalizeWA(fmt.Sprint(u["phone"]))
		}
	}

	fName := "Academy"
	_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(name,'') FROM app_facilities WHERE id=$1`, facilityID).Scan(&fName)
	fName = strings.TrimSpace(fName)
	if fName == "" {
		fName = "Academy"
	}

	parsed := parseBookingSlots(slots)
	if len(parsed) == 0 {
		return
	}

	p1, p2, p3, p4, p5, p6, p7 := buildBookingWAParams(
		uName, bookingID, fName, parsed, paymentMode, transactionID,
		paidAmount, discountTotal, totalBill, balanceDue,
	)

	title := "Booking confirmed"
	mode := strings.ToLower(strings.TrimSpace(paymentMode))
	txn := strings.TrimSpace(transactionID)
	switch {
	case mode == "spot" || strings.HasPrefix(strings.ToUpper(txn), "SPOT_"):
		title = "Spot booking confirmed"
	case paidAmount <= 0:
		title = "Free booking confirmed"
	case mode == "advance":
		title = "Advance payment confirmed"
	}
	body := fmt.Sprintf("%s · %s · %s", fName, strings.TrimPrefix(p5, "🗓️ "), strings.TrimPrefix(p6, "⏰ "))
	if paidAmount > 0 {
		body = fmt.Sprintf("₹%.0f paid · %s", paidAmount, body)
	}
	metaRaw, _ := json.Marshal(map[string]any{
		"source":        "booking_confirm",
		"bookingId":     bookingID,
		"facilityId":    facilityID,
		"paymentMode":   paymentMode,
		"transactionId": transactionID,
		"paidAmount":    paidAmount,
		"audience":      "booking_user",
	})

	s.notifyFacilityStaffPush(ctx, facilityID, title, body, bookingID)
	pushErr := s.notifyBookingPush(ctx, facilityID, userID, title, body, bookingID)
	if pushErr != nil {
		log.Printf("booking notify push: booking=%d err=%v", bookingID, pushErr)
		if err := s.writeBookingInbox(ctx, userID, facilityID, bookingID, title, body, string(metaRaw)); err != nil {
			log.Printf("booking notify inbox: booking=%d err=%v", bookingID, err)
		}
	}

	params := []string{p1, p2, p3, p4, p5, p6, p7}
	if uPhone == "" {
		log.Printf("booking notify WA skipped: booking=%d no phone", bookingID)
	} else if err := s.notifyBookingWhatsApp(ctx, facilityID, userID, uPhone, params); err != nil {
		log.Printf("booking notify WA: booking=%d err=%v", bookingID, err)
	}
	s.notifyFacilityBookingWhatsApp(ctx, facilityID, userID, "booking_confirmation", title, params)
}

type bookingSlotParts struct {
	court string
	date  string
	time  string
}

func parseBookingSlots(slots []any) []bookingSlotParts {
	out := make([]bookingSlotParts, 0, len(slots))
	for _, sl := range slots {
		m, _ := sl.(map[string]any)
		if m == nil {
			continue
		}
		court := firstNonEmpty(fmt.Sprint(m["court"]), fmt.Sprint(m["courtName"]), fmt.Sprint(m["subFacilityName"]))
		date := firstNonEmpty(fmt.Sprint(m["date"]), fmt.Sprint(m["slotDate"]), fmt.Sprint(m["slot_date"]))
		tr := firstNonEmpty(fmt.Sprint(m["time"]), fmt.Sprint(m["timeRange"]), fmt.Sprint(m["time_range"]))
		if court == "" || date == "" || tr == "" {
			continue
		}
		out = append(out, bookingSlotParts{court: court, date: date, time: tr})
	}
	return out
}

func buildBookingWAParams(
	uName string,
	bookingID int64,
	fName string,
	slots []bookingSlotParts,
	paymentMode, transactionID string,
	paidAmount, discountTotal, totalBill, balanceDue float64,
) (p1, p2, p3, p4, p5, p6, p7 string) {
	first := slots[0]
	dateArr := []string{}
	slotSummary := []string{}
	seenDate := map[string]struct{}{}
	uniqueDates := []string{}
	for _, s := range slots {
		d := formatSlotDayMonth(s.date)
		dateArr = append(dateArr, d)
		slotSummary = append(slotSummary, d+" ("+s.time+")")
		if _, ok := seenDate[d]; !ok {
			seenDate[d] = struct{}{}
			uniqueDates = append(uniqueDates, d)
		}
	}

	p1 = uName
	p2 = "🧾 " + strconv.FormatInt(bookingID, 10)
	p3 = "📍 " + fName
	p4 = "🏟️ " + first.court
	if len(uniqueDates) == 1 {
		p5 = "🗓️ " + formatSlotDayMonthWeekday(first.date)
		times := make([]string, 0, len(slots))
		for _, s := range slots {
			times = append(times, s.time)
		}
		p6 = "⏰ " + strings.Join(times, ", ")
	} else {
		p5 = "🗓️ " + strings.Join(uniqueDates, ", ")
		p6 = "⏰ " + strings.Join(slotSummary, " | ")
	}

	// PHP webhook_razorpay / booking_confirmation Payment details (p7).
	p7 = formatBookingPaymentDetails(paymentMode, transactionID, paidAmount, discountTotal, totalBill, balanceDue)
	return
}

// formatBookingPaymentDetails matches production WA:
// "💰 Total: ₹1050 | 💵 Paid: ₹3 | ⚠️ Bal Due: ₹1047 | Ref: pay_…"
func formatBookingPaymentDetails(
	paymentMode, transactionID string,
	paidAmount, discountTotal, totalBill, balanceDue float64,
) string {
	mode := strings.ToLower(strings.TrimSpace(paymentMode))
	txn := strings.TrimSpace(transactionID)
	total := totalBill
	if total <= 0 && discountTotal > 0 {
		total = discountTotal
	}
	if total <= 0 && paidAmount > 0 {
		total = paidAmount
	}
	bal := balanceDue
	if bal < 0 {
		bal = 0
	}
	// Recompute bal if caller didn't pass (legacy).
	if total > 0 && bal <= 0 && paidAmount+0.009 < total && mode != "free" {
		if !(discountTotal > 0 && paidAmount <= 0) {
			bal = total - paidAmount
			if bal < 0 {
				bal = 0
			}
		}
	}

	isSpot := mode == "spot" || strings.HasPrefix(strings.ToUpper(txn), "SPOT_")
	if paidAmount <= 0 && discountTotal > 0 && !isSpot {
		hint := total
		if hint <= 0 {
			hint = discountTotal
		}
		return fmt.Sprintf(
			"💰 Total: ₹%.0f | 💵 100%% Off (Offer Applied) | ✅ Fully Paid | Ref: %s",
			hint, txn,
		)
	}
	if isSpot && paidAmount <= 0 {
		if total <= 0 {
			return "💰 Spot booking | Pay at venue | Ref: " + txn
		}
		return fmt.Sprintf(
			"💰 Total: ₹%.0f | 💵 Paid: ₹0 | ⚠️ Bal Due: ₹%.0f | Ref: %s",
			total, total, txn,
		)
	}
	if paidAmount <= 0 && total <= 0 {
		return "💰 Free booking | ✅ Confirmed | Ref: " + txn
	}
	balStatus := "✅ Fully Paid"
	if bal > 0.5 {
		balStatus = fmt.Sprintf("⚠️ Bal Due: ₹%.0f", bal)
	}
	return fmt.Sprintf(
		"💰 Total: ₹%.0f | 💵 Paid: ₹%.0f | %s | Ref: %s",
		total, paidAmount, balStatus, txn,
	)
}

func formatSlotDayMonth(raw string) string {
	t, ok := parseSlotDate(raw)
	if !ok {
		return raw
	}
	return t.Format("02 Jan")
}

func formatSlotDayMonthWeekday(raw string) string {
	t, ok := parseSlotDate(raw)
	if !ok {
		return raw
	}
	return t.Format("02 Jan, Mon")
}

func parseSlotDate(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	layouts := []string{
		"2006-01-02",
		time.RFC3339,
		"02 Jan 2006",
		"02-01-2006",
		"2006/01/02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	// date-only prefix from timestamps
	if len(raw) >= 10 {
		if t, err := time.Parse("2006-01-02", raw[:10]); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func (s *Service) writeBookingInbox(ctx context.Context, userID, facilityID, bookingID int64, title, body, metaJSON string) error {
	if metaJSON == "" {
		metaJSON = "{}"
	}
	bid := strconv.FormatInt(bookingID, 10)
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO app_notifications (user_id, facility_id, booking_id, ntype, title, body, source, meta_json)
VALUES ($1,$2,$3,'booking_user_notify',$4,$5,'booking',$6::jsonb)`,
		userID, facilityID, bid, title, body, metaJSON)
	return err
}

func (s *Service) notifyFacilityStaffPush(ctx context.Context, facilityID int64, title, body string, bookingID int64) {
	if facilityID <= 0 {
		return
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT DISTINCT m.user_id
FROM user_facility_memberships m
WHERE m.facility_id=$1 AND m.status='active' AND m.user_id>0
  AND lower(replace(replace(m.role, ' ', '_'), '-', '_')) IN ('admin', 'sub_admin', 'subadmin')`, facilityID)
	if err != nil {
		log.Printf("facility staff push: facility=%d err=%v", facilityID, err)
		return
	}
	defer rows.Close()
	seen := map[int64]struct{}{}
	for rows.Next() {
		var staffID int64
		if rows.Scan(&staffID) != nil || staffID <= 0 {
			continue
		}
		// StrollX login only. Do not map this user onto a Proximity app account.
		if _, ok := seen[staffID]; ok {
			continue
		}
		seen[staffID] = struct{}{}
		if err := s.notifyStaffPush(ctx, facilityID, staffID, title, body, bookingID); err != nil {
			log.Printf("facility staff push: facility=%d user=%d err=%v", facilityID, staffID, err)
		}
	}
}

func (s *Service) notifyStaffPush(ctx context.Context, facilityID, userID int64, title, body string, bookingID int64) error {
	base := strings.TrimRight(strings.TrimSpace(s.Cfg.NotificationsURL), "/")
	if base == "" {
		return fmt.Errorf("notifications_url_missing")
	}
	payload := map[string]any{
		"channel":     "push",
		"facility_id": facilityID,
		"template":    "booking_staff_notify",
		"to":          map[string]any{"user_id": userID},
		"data": map[string]any{
			"title":     title,
			"body":      body,
			"type":      "booking_staff_notify",
			"bookingId": strconv.FormatInt(bookingID, 10),
			"audience":  "management",
			"app":       "strollx",
		},
	}
	return s.postNotificationsSend(ctx, base, payload)
}

func (s *Service) notifyBookingPush(ctx context.Context, facilityID, userID int64, title, body string, bookingID int64) error {
	base := strings.TrimRight(strings.TrimSpace(s.Cfg.NotificationsURL), "/")
	if base == "" {
		return fmt.Errorf("notifications_url_missing")
	}
	payload := map[string]any{
		"channel":     "push",
		"facility_id": facilityID,
		"template":    "booking_user_notify",
		"to":          map[string]any{"user_id": userID},
		"data": map[string]any{
			"title":     title,
			"body":      body,
			"type":      "booking_user_notify",
			"bookingId": strconv.FormatInt(bookingID, 10),
			"audience":  "booking_user",
		},
	}
	return s.postNotificationsSend(ctx, base, payload)
}

func (s *Service) notifyBookingWhatsApp(ctx context.Context, facilityID, userID int64, phone string, bodyParams []string) error {
	return s.notifyBookingWhatsAppTemplate(ctx, facilityID, userID, phone, "booking_confirmation", "Booking confirmed", bodyParams)
}

func (s *Service) facilityWhatsApp(ctx context.Context, facilityID int64) string {
	if facilityID <= 0 {
		return ""
	}
	var wa string
	err := s.DB.QueryRowContext(ctx, `
SELECT COALESCE(NULLIF(whatsapp,''), NULLIF(phone,''), '')
FROM facility_profiles WHERE facility_id=$1`, facilityID).Scan(&wa)
	if err != nil {
		return ""
	}
	return normalizeWA(wa)
}

func (s *Service) notifyFacilityBookingWhatsApp(ctx context.Context, facilityID, userID int64, template, title string, bodyParams []string) {
	facPhone := s.facilityWhatsApp(ctx, facilityID)
	if facPhone == "" {
		return
	}
	if err := s.notifyBookingWhatsAppTemplate(ctx, facilityID, userID, facPhone, template, title, bodyParams); err != nil {
		log.Printf("facility booking WA: facility=%d template=%s err=%v", facilityID, template, err)
	}
}

func (s *Service) notifyBookingWhatsAppTemplate(
	ctx context.Context,
	facilityID, userID int64,
	phone, template, title string,
	bodyParams []string,
) error {
	base := strings.TrimRight(strings.TrimSpace(s.Cfg.NotificationsURL), "/")
	if base == "" {
		return fmt.Errorf("notifications_url_missing")
	}
	tmpl := strings.TrimSpace(template)
	if tmpl == "" {
		tmpl = "booking_confirmation"
	}
	if title == "" {
		title = tmpl
	}
	// Callers already send FCM separately — keep WhatsApp-only here.
	payload := map[string]any{
		"channel":     "whatsapp",
		"facility_id": facilityID,
		"template":    tmpl,
		"to": map[string]any{
			"whatsapp": phone,
			"user_id":  userID,
		},
		"data": map[string]any{
			"body_params": bodyParams,
			"title":       title,
			"type":        tmpl,
		},
	}
	return s.postNotificationsSend(ctx, base, payload)
}

// Free-form WhatsApp (PHP sendWhatsappMessage) — works inside 24h session window.
func (s *Service) notifyBookingWhatsAppText(
	ctx context.Context,
	facilityID, userID int64,
	phone, title, message string,
) error {
	base := strings.TrimRight(strings.TrimSpace(s.Cfg.NotificationsURL), "/")
	if base == "" {
		return fmt.Errorf("notifications_url_missing")
	}
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("empty_message")
	}
	if title == "" {
		title = "WhatsApp"
	}
	// Callers already send FCM separately — keep WhatsApp-only here.
	payload := map[string]any{
		"channel":     "whatsapp",
		"facility_id": facilityID,
		"template":    "text",
		"to": map[string]any{
			"whatsapp": phone,
			"user_id":  userID,
		},
		"data": map[string]any{
			"message_type": "text",
			"message":      message,
			"title":        title,
			"body":         message,
			"type":         "text",
		},
	}
	return s.postNotificationsSend(ctx, base, payload)
}

func (s *Service) postNotificationsSend(ctx context.Context, base string, payload map[string]any) error {
	raw, _ := json.Marshal(payload)
	reqCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, base+"/v1/notifications/send", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Cfg.NotificationsKey != "" {
		req.Header.Set("X-Service-Key", s.Cfg.NotificationsKey)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("notify_http_%d: %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	var job map[string]any
	_ = json.Unmarshal(b, &job)
	if st, _ := job["status"].(string); strings.EqualFold(st, "failed") {
		errText, _ := job["error_text"].(string)
		if errText == "" {
			errText = "notify_failed"
		}
		return fmt.Errorf("%s", errText)
	}
	return nil
}

func paidAmountFromBody(body map[string]any) float64 {
	for _, key := range []string{"amount", "finalAmount", "paidAmount", "totalAmountPaid", "amountPaid"} {
		if v, err := strconv.ParseFloat(fmt.Sprint(body[key]), 64); err == nil && v > 0 {
			return v
		}
	}
	return 0
}
