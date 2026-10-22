package booking

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// notifyFacilityBooking sends the existing customer template to that facility's WhatsApp only.
func (s *Service) notifyFacilityBooking(ctx context.Context, b *Booking, cancelled bool) {
	if b == nil || b.FacilityID <= 0 {
		return
	}
	phone, facilityName := s.facilityAlertWhatsApp(ctx, b.FacilityID)
	name := strings.TrimSpace(b.CustomerName)
	if name == "" {
		name = "Customer"
	}
	if facilityName == "" {
		facilityName = "Academy"
	}
	court := strings.TrimSpace(b.CourtName)
	if court == "" {
		court = "Court"
	}
	dateStr := strings.TrimSpace(b.SlotDate)
	if len(dateStr) >= 10 {
		dateStr = dateStr[:10]
	}
	if dateStr == "" {
		dateStr = "—"
	}
	timeStr := clockHHMM(b.StartTime) + "-" + clockHHMM(b.EndTime)
	idText := fmt.Sprintf("%d", b.ID)
	template := "booking_confirmation"
	title := "Booking confirmed"
	var params []string
	if cancelled {
		template = "booking_cancellation"
		title = "Booking cancelled"
		params = []string{
			name,
			"🧾 " + idText,
			"📍 " + facilityName,
			"🏟️ " + court,
			"🗓️ " + dateStr,
			"❌ Cancelled · " + timeStr + " | Ref: " + idText,
		}
	} else {
		pay := "💰 Booked | Ref: " + idText
		if b.AmountPaid != nil && *b.AmountPaid > 0 {
			pay = fmt.Sprintf("💵 Paid: ₹%.0f | Ref: %s", *b.AmountPaid, idText)
		}
		params = []string{
			name,
			"🧾 " + idText,
			"📍 " + facilityName,
			"🏟️ " + court,
			"🗓️ " + dateStr,
			"⏰ " + timeStr,
			pay,
		}
	}
	if phone == "" {
		log.Printf("facility booking WA skipped: facility=%d no whatsapp", b.FacilityID)
	} else if err := s.sendFacilityTemplate(ctx, b.FacilityID, phone, template, title, params); err != nil {
		log.Printf("facility booking WA: booking=%d template=%s err=%v", b.ID, template, err)
	}
	pushBody := fmt.Sprintf("%s · %s · %s · %s", name, court, dateStr, timeStr)
	s.notifyFacilityStaffPush(ctx, b.FacilityID, title, pushBody, b.ID)
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
		targets := []int64{staffID}
		mapped, merr := s.DB.QueryContext(ctx, `
SELECT DISTINCT users_user_id FROM app_fcm_audience_map
WHERE audience='management' AND role_id=$1 AND users_user_id>0`, staffID)
		if merr == nil {
			for mapped.Next() {
				var appUser int64
				if mapped.Scan(&appUser) == nil && appUser > 0 {
					targets = append(targets, appUser)
				}
			}
			mapped.Close()
		}
		for _, uid := range targets {
			if _, ok := seen[uid]; ok {
				continue
			}
			seen[uid] = struct{}{}
			if err := s.sendStaffPush(ctx, facilityID, uid, title, body, bookingID); err != nil {
				log.Printf("facility staff push: facility=%d user=%d err=%v", facilityID, uid, err)
			}
		}
	}
}

func (s *Service) sendStaffPush(ctx context.Context, facilityID, userID int64, title, body string, bookingID int64) error {
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
			"bookingId": fmt.Sprintf("%d", bookingID),
			"audience":  "management",
		},
	}
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
	resp, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("notify_http_%d %s", res.StatusCode, strings.TrimSpace(string(resp)))
	}
	return nil
}

func (s *Service) facilityAlertWhatsApp(ctx context.Context, facilityID int64) (string, string) {
	var wa, name string
	err := s.DB.QueryRowContext(ctx, `
SELECT COALESCE(NULLIF(whatsapp,''), NULLIF(phone,''), ''),
       COALESCE(display_name, '')
FROM facility_profiles WHERE facility_id=$1`, facilityID).Scan(&wa, &name)
	if err != nil {
		return "", ""
	}
	return normalizeWhatsApp(wa), strings.TrimSpace(name)
}

func (s *Service) sendFacilityTemplate(ctx context.Context, facilityID int64, phone, template, title string, bodyParams []string) error {
	base := strings.TrimRight(strings.TrimSpace(s.Cfg.NotificationsURL), "/")
	if base == "" {
		return fmt.Errorf("notifications_url_missing")
	}
	payload := map[string]any{
		"channel":     "whatsapp",
		"facility_id": facilityID,
		"template":    template,
		"to":          map[string]any{"whatsapp": phone},
		"data": map[string]any{
			"body_params": bodyParams,
			"title":       title,
			"type":        template,
		},
	}
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
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("notify_http_%d %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func clockHHMM(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 5 {
		return raw[:5]
	}
	if raw == "" {
		return "—"
	}
	return raw
}
