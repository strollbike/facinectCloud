package app

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// mirrorAppBookingToLedger copies a Proximity confirm into booking_bookings
// so StrollX View Bookings shows the same slot.
func (s *Service) mirrorAppBookingToLedger(ctx context.Context, facilityID, sportID, userID, appBookingID int64, amount float64, payMode string, slots []any) {
	if facilityID <= 0 || appBookingID <= 0 {
		return
	}
	name, phone := "Customer", ""
	if u, _ := s.userRow(ctx, userID, ""); u != nil {
		if v := strings.TrimSpace(fmt.Sprint(u["fullName"])); v != "" && v != "<nil>" {
			name = v
		}
		phone = strings.TrimSpace(fmt.Sprint(u["whatsappNo"]))
		if phone == "" || phone == "<nil>" {
			phone = strings.TrimSpace(fmt.Sprint(u["phone"]))
		}
		if phone == "<nil>" {
			phone = ""
		}
	}
	payStatus := "unpaid"
	mode := strings.ToLower(strings.TrimSpace(payMode))
	if amount > 0 && mode != "spot" && mode != "free" {
		payStatus = "paid"
	}
	note := fmt.Sprintf("Booking via App #%d", appBookingID)
	first := true
	for _, k := range parseSlotKeys(slots, sportID) {
		start := slotStartHHMM(k.time)
		if start == "" {
			continue
		}
		sport := k.sport
		if sport <= 0 {
			sport = sportID
		}
		var slotID int64
		err := s.DB.QueryRowContext(ctx, `
SELECT s.id
FROM booking_slots s
JOIN booking_courts c ON c.id = s.court_id
WHERE s.facility_id=$1
  AND lower(c.name)=lower($2)
  AND s.slot_date=$3::date
  AND to_char(s.start_time, 'HH24:MI')=$4
  AND ($5=0 OR c.sport_id IS NULL OR c.sport_id=0 OR c.sport_id=$5)
  AND lower(s.status)='available'
  AND NOT EXISTS (
    SELECT 1 FROM booking_bookings b
    WHERE b.slot_id=s.id AND b.status='confirmed'
  )
ORDER BY s.id
LIMIT 1`, facilityID, k.court, k.date, start, sport).Scan(&slotID)
		if err != nil || slotID <= 0 {
			log.Printf("proximity ledger skip: facility=%d court=%s date=%s start=%s err=%v", facilityID, k.court, k.date, start, err)
			continue
		}
		rowAmount := 0.0
		rowStatus := "unpaid"
		if first {
			rowAmount = amount
			rowStatus = payStatus
			first = false
		}
		_, err = s.DB.ExecContext(ctx, `
INSERT INTO booking_bookings (
  facility_id, slot_id, user_id, customer_name, customer_phone, status, notes,
  payment_status, pay_mode, amount_paid
) VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),'confirmed',$6,$7,NULLIF($8,''),$9)`,
			facilityID, slotID, userID, name, phone, note, rowStatus, mode, rowAmount)
		if err != nil {
			log.Printf("proximity ledger insert: facility=%d slot=%d err=%v", facilityID, slotID, err)
			continue
		}
		_, _ = s.DB.ExecContext(ctx, `
UPDATE booking_slots SET status='booked', updated_at=NOW()
WHERE id=$1 AND lower(status)='available'`, slotID)
	}
}

func (s *Service) cancelLedgerForAppBooking(ctx context.Context, facilityID, appBookingID int64) {
	if facilityID <= 0 || appBookingID <= 0 {
		return
	}
	note := fmt.Sprintf("Booking via App #%d", appBookingID)
	_, err := s.DB.ExecContext(ctx, `
UPDATE booking_bookings
SET status='cancelled', updated_at=NOW()
WHERE facility_id=$1 AND status='confirmed' AND notes=$2`, facilityID, note)
	if err != nil {
		log.Printf("proximity ledger cancel: facility=%d app_booking=%d err=%v", facilityID, appBookingID, err)
		return
	}
	_, _ = s.DB.ExecContext(ctx, `
UPDATE booking_slots s
SET status='available', updated_at=NOW()
WHERE s.facility_id=$1
  AND lower(s.status)='booked'
  AND EXISTS (
    SELECT 1 FROM booking_bookings b
    WHERE b.slot_id=s.id AND b.facility_id=$1 AND b.notes=$2 AND b.status='cancelled'
  )
  AND NOT EXISTS (
    SELECT 1 FROM booking_bookings b
    WHERE b.slot_id=s.id AND b.status='confirmed'
  )`, facilityID, note)
}

func slotStartHHMM(timeRange string) string {
	part := strings.TrimSpace(timeRange)
	if i := strings.Index(part, "-"); i >= 0 {
		part = strings.TrimSpace(part[:i])
	}
	if len(part) >= 5 {
		return part[:5]
	}
	return part
}
