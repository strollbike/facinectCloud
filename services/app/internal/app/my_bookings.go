package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
)

const bookingCancelLead = 15 * time.Minute

func bookingTZ() *time.Location {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		return time.Local
	}
	return loc
}

type bookingSlotMeta struct {
	Start       time.Time
	Courts      string
	FormattedDate string
	FormattedTime string
	TimesJoined string
}

// bookingMetaFromSlots derives display fields + earliest slot start (PHP bookingTime).
func bookingMetaFromSlots(slots []any) bookingSlotMeta {
	loc := bookingTZ()
	parsed := parseBookingSlots(slots)
	meta := bookingSlotMeta{}
	if len(parsed) == 0 {
		return meta
	}
	courts := []string{}
	times := []string{}
	seenCourt := map[string]struct{}{}
	var earliest time.Time
	for _, p := range parsed {
		if _, ok := seenCourt[p.court]; !ok && p.court != "" {
			seenCourt[p.court] = struct{}{}
			courts = append(courts, p.court)
		}
		if p.time != "" {
			times = append(times, p.time)
		}
		start, ok := combineSlotStart(p.date, p.time, loc)
		if !ok {
			continue
		}
		if earliest.IsZero() || start.Before(earliest) {
			earliest = start
		}
	}
	meta.Courts = strings.Join(courts, ", ")
	meta.TimesJoined = strings.Join(times, ", ")
	meta.Start = earliest
	if !earliest.IsZero() {
		meta.FormattedDate = earliest.Format("Mon, 02 Jan 2006")
		if meta.TimesJoined != "" {
			meta.FormattedTime = meta.TimesJoined
		} else {
			meta.FormattedTime = earliest.Format("03:04 PM")
		}
	}
	return meta
}

func bookingMetaFromSlotsJSON(raw []byte) bookingSlotMeta {
	if len(raw) == 0 {
		return bookingSlotMeta{}
	}
	var slots []any
	if err := json.Unmarshal(raw, &slots); err != nil {
		return bookingSlotMeta{}
	}
	return bookingMetaFromSlots(slots)
}

func combineSlotStart(dateRaw, timeRange string, loc *time.Location) (time.Time, bool) {
	day, ok := parseSlotDate(dateRaw)
	if !ok {
		return time.Time{}, false
	}
	// reinterpret calendar day in facility TZ
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	startClock := slotRangeStart(timeRange)
	if startClock == "" {
		return day, true
	}
	hh, mm, ok := parseClockHM(startClock)
	if !ok {
		return day, true
	}
	return time.Date(day.Year(), day.Month(), day.Day(), hh, mm, 0, 0, loc), true
}

func slotRangeStart(tr string) string {
	tr = strings.TrimSpace(tr)
	if tr == "" {
		return ""
	}
	for _, sep := range []string{" - ", " – ", "—", "-", "–", " to ", " TO "} {
		if i := strings.Index(strings.ToLower(tr), strings.ToLower(sep)); i > 0 {
			return strings.TrimSpace(tr[:i])
		}
	}
	return tr
}

func parseClockHM(raw string) (int, int, bool) {
	raw = strings.TrimSpace(raw)
	upper := strings.ToUpper(raw)
	layouts := []string{"3:04 PM", "3:04PM", "15:04", "3:04", "15:04:05"}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, upper); err == nil {
			return t.Hour(), t.Minute(), true
		}
		if t, err := time.Parse(layout, raw); err == nil {
			return t.Hour(), t.Minute(), true
		}
	}
	return 0, 0, false
}

func isUpcomingBooking(status string, start time.Time, now time.Time) bool {
	if !strings.EqualFold(strings.TrimSpace(status), "confirmed") {
		return false
	}
	if start.IsZero() {
		return false
	}
	return start.Sub(now) > bookingCancelLead
}

func (s *Service) sportName(ctx context.Context, sportID int64) string {
	if sportID <= 0 {
		return "Other"
	}
	var name string
	_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(name,'') FROM app_sports WHERE id=$1`, sportID).Scan(&name)
	name = strings.TrimSpace(name)
	if name == "" {
		return "Other"
	}
	return name
}

func bookingListItem(id, facilityID, sportID int64, fname, sportName, status string, start time.Time, courts, ftime, fdate string) map[string]any {
	if fdate == "" && !start.IsZero() {
		fdate = start.Format("Mon, 02 Jan 2006")
	}
	if ftime == "" && !start.IsZero() {
		ftime = start.Format("03:04 PM")
	}
	uiDay, uiMonth, uiWeekday := "", "", ""
	rawUnix := int64(0)
	if !start.IsZero() {
		uiDay = fmt.Sprintf("%02d", start.Day())
		uiMonth = start.Format("Jan")
		uiWeekday = start.Weekday().String()
		rawUnix = start.Unix()
	}
	return map[string]any{
		"bookingId":         strconv.FormatInt(id, 10),
		"facilityId":        facilityID,
		"facilityName":      fname,
		"sportId":           sportID,
		"sportName":         sportName,
		"subFacilityNames":  courts,
		"bookingTime":       start.Format(time.RFC3339),
		"formatted_time":    ftime,
		"formatted_date":    fdate,
		"ui_day":            uiDay,
		"ui_month":          uiMonth,
		"ui_weekday":        uiWeekday,
		"raw_timestamp":     rawUnix,
		"status":            status,
	}
}

func (s *Service) releaseBookingSlots(ctx context.Context, facilityID, sportID int64, slotsJSON []byte) {
	var slots []any
	if err := json.Unmarshal(slotsJSON, &slots); err != nil {
		return
	}
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
		_, _ = s.DB.ExecContext(ctx, `
UPDATE app_slots
SET status='available', held_by_user_id=NULL, onhold_update_time=NULL
WHERE facility_id=$1 AND ($2=0 OR sport_id=$2)
  AND court_name=$3 AND slot_date=$4::date AND time_range=$5`,
			facilityID, sportID, court, date, tr)
	}
}

func (s *Service) notifyBookingCancelled(
	ctx context.Context,
	userID, facilityID, sportID, bookingID int64,
	facilityName string,
	slotsJSON []byte,
	refundCoupon string,
	refundAmount float64,
	isRegOffer bool,
) {
	title := "Booking cancelled"
	body := strings.TrimSpace(facilityName) + " — booking #" + strconv.FormatInt(bookingID, 10) + " cancelled"
	if refundCoupon != "" {
		body += ". Refund coupon: " + refundCoupon
		if refundAmount > 0 {
			body += fmt.Sprintf(" (₹%.0f)", refundAmount)
		}
	}
	metaRaw, _ := json.Marshal(map[string]any{
		"source":     "booking_cancel",
		"bookingId":  bookingID,
		"facilityId": facilityID,
		"coupon":     refundCoupon,
		"amount":     refundAmount,
		"audience":   "booking_user",
	})
	bid := strconv.FormatInt(bookingID, 10)
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO app_notifications (user_id, facility_id, booking_id, ntype, title, body, source, meta_json)
VALUES ($1,$2,$3,'booking_cancelled',$4,$5,'booking',$6::jsonb)`,
		userID, facilityID, bid, title, body, string(metaRaw))
	if err != nil {
		log.Printf("booking cancel inbox: booking=%d err=%v", bookingID, err)
	}
	if err := s.notifyBookingPush(ctx, facilityID, userID, title, body, bookingID); err != nil {
		log.Printf("booking cancel push: booking=%d err=%v", bookingID, err)
	}

	u, _ := s.userRow(ctx, userID, "")
	uPhone := ""
	if u != nil {
		uPhone = normalizeWA(fmt.Sprint(u["whatsappNo"]))
		if uPhone == "" {
			uPhone = normalizeWA(fmt.Sprint(u["phone"]))
		}
	}
	if uPhone == "" {
		log.Printf("booking cancel WA skipped: booking=%d no phone", bookingID)
		return
	}

	sportName := "Sport"
	if sportID > 0 {
		_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(name,'Sport') FROM app_sports WHERE id=$1`, sportID).Scan(&sportName)
		sportName = strings.TrimSpace(sportName)
		if sportName == "" {
			sportName = "Sport"
		}
	}

	var slots []any
	_ = json.Unmarshal(slotsJSON, &slots)
	parsed := parseBookingSlots(slots)
	dateStr, timeStr := "—", "—"
	if len(parsed) > 0 {
		dateStr = formatCancelDateLong(parsed[0].date)
		times := make([]string, 0, len(parsed))
		for _, p := range parsed {
			times = append(times, p.time)
		}
		timeStr = strings.Join(times, ", ")
	}

	// PHP my_bookings.php free-form cancellation notice (not Meta template).
	msg := buildCancelWhatsAppMessage(bid, sportName, dateStr, timeStr, refundCoupon, isRegOffer)
	if err := s.notifyBookingWhatsAppText(ctx, facilityID, userID, uPhone, title, msg); err != nil {
		log.Printf("booking cancel WA text: booking=%d err=%v — falling back to template", bookingID, err)
		// Fallback: 6-param template if session text fails (outside 24h window).
		tmpl := strings.TrimSpace(s.Cfg.BookingCancelWhatsAppTemplate)
		if tmpl == "" {
			tmpl = "booking_cancellation"
		}
		uName := "Customer"
		if u != nil {
			if v := strings.TrimSpace(fmt.Sprint(u["fullName"])); v != "" && v != "<nil>" {
				uName = v
			}
		}
		fName := strings.TrimSpace(facilityName)
		if fName == "" {
			fName = "Academy"
		}
		var bodyParams []string
		if len(parsed) > 0 {
			p1, p2, p3, p4, p5, p6, _ := buildBookingWAParams(
				uName, bookingID, fName, parsed, "cancelled", "CANCEL_"+bid, 0, 0, 0, 0,
			)
			timePart := strings.TrimSpace(strings.TrimPrefix(p6, "⏰ "))
			tail := "❌ Cancelled · " + timePart + " | Ref: " + bid
			if refundCoupon != "" {
				tail = "❌ Cancelled · " + timePart + " | Coupon: " + refundCoupon + " | Ref: " + bid
			}
			bodyParams = []string{p1, p2, p3, p4, p5, tail}
		} else {
			tail := "❌ Cancelled | Ref: " + bid
			if refundCoupon != "" {
				tail = "❌ Cancelled | Coupon: " + refundCoupon + " | Ref: " + bid
			}
			bodyParams = []string{uName, "🧾 " + bid, "📍 " + fName, "🏟️ Court", "🗓️ —", tail}
		}
		if err2 := s.notifyBookingWhatsAppTemplate(ctx, facilityID, userID, uPhone, tmpl, title, bodyParams); err2 != nil {
			log.Printf("booking cancel WA template: booking=%d err=%v", bookingID, err2)
		}
	}
}

func buildCancelWhatsAppMessage(bookingID, sportName, dateStr, timeStr, refundCoupon string, isRegOffer bool) string {
	var b strings.Builder
	b.WriteString("🔔 *Booking Cancellation Notice*\n\n")
	b.WriteString("Your booking has been successfully cancelled.\n\n")
	b.WriteString("───────────────────\n")
	b.WriteString("🧾 *Booking ID:* #" + bookingID + "\n")
	b.WriteString("🏆 *Sport:* " + sportName + "\n")
	b.WriteString("📅 *Date:* " + dateStr + "\n")
	b.WriteString("⏰ *Time:* " + timeStr + "\n")
	b.WriteString("───────────────────")
	if refundCoupon != "" {
		b.WriteString("\n\n🎟️ *Refund Coupon:*\n```" + refundCoupon + "```")
	} else if isRegOffer {
		b.WriteString("\n\n⚠️ *Note:* This booking was made using a special offer, so no refund coupon is issued.")
	}
	return b.String()
}

func formatCancelDateLong(raw string) string {
	t, ok := parseSlotDate(raw)
	if !ok {
		return raw
	}
	return t.Format("Mon, 02 Jan 2006")
}

func sortBookingsByStart(items []map[string]any, ascending bool) {
	sort.SliceStable(items, func(i, j int) bool {
		ti, _ := items[i]["raw_timestamp"].(int64)
		tj, _ := items[j]["raw_timestamp"].(int64)
		if ascending {
			return ti < tj
		}
		return ti > tj
	})
}
