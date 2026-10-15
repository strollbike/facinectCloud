package app

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type priceRow struct {
	court    string
	dayType  string
	price    float64
	advance  float64
}

func normalizePriceDayType(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, " ", "")
	switch s {
	case "WEEKEND", "WEEKENDS", "SATURDAY", "SUNDAY":
		return "WEEKEND"
	case "WEEKDAY", "WEEKDAYS", "ALL", "ALLDAYS", "EVERYDAY":
		if s == "ALL" || s == "ALLDAYS" || s == "EVERYDAY" {
			return "ALLDAYS"
		}
		return "WEEKDAY"
	default:
		return s
	}
}

func dayTypeForSlotDate(dateRaw string) string {
	t, ok := parseSlotDate(dateRaw)
	if !ok {
		return "WEEKDAY"
	}
	wd := t.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return "WEEKEND"
	}
	return "WEEKDAY"
}

func (s *Service) loadPriceRows(ctx context.Context, facilityID, sportID int64) []priceRow {
	rows, err := s.DB.QueryContext(ctx, `
SELECT court_name, day_type, price, advance_amount
FROM app_price_list
WHERE facility_id=$1 AND ($2=0 OR sport_id=$2)`, facilityID, sportID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []priceRow{}
	for rows.Next() {
		var court, day string
		var price, adv float64
		if rows.Scan(&court, &day, &price, &adv) != nil {
			continue
		}
		out = append(out, priceRow{
			court:   strings.TrimSpace(court),
			dayType: normalizePriceDayType(day),
			price:   price,
			advance: adv,
		})
	}
	return out
}

func matchSlotPrice(rows []priceRow, court, dayType string) (price, advance float64, ok bool) {
	court = strings.TrimSpace(court)
	dayType = normalizePriceDayType(dayType)
	// Exact court + day
	for _, r := range rows {
		if !strings.EqualFold(r.court, court) {
			continue
		}
		if r.dayType == dayType {
			return r.price, r.advance, true
		}
	}
	// Court + ALLDAYS
	for _, r := range rows {
		if strings.EqualFold(r.court, court) && r.dayType == "ALLDAYS" {
			return r.price, r.advance, true
		}
	}
	// Any row for court (fallback)
	for _, r := range rows {
		if strings.EqualFold(r.court, court) {
			return r.price, r.advance, true
		}
	}
	return 0, 0, false
}

// computeCheckoutAmounts mirrors Flutter BookingPaymentOptions subtotal / payableAdvance.
// Returns totalBill, payableAdvance (0 if advance not applicable), and sport-level advance unit.
func (s *Service) computeCheckoutAmounts(ctx context.Context, facilityID, sportID int64, slots []any) (total, advancePay, sportAdvance float64) {
	keys := parseSlotKeys(slots, sportID)
	rows := s.loadPriceRows(ctx, facilityID, sportID)
	if len(keys) == 0 {
		return 0, 0, 0
	}

	// Sport-level advance from first matching / max advance_amount column on list.
	for _, r := range rows {
		if r.advance > sportAdvance {
			sportAdvance = r.advance
		}
	}
	// Also read Booking-style facility sport advance if present on any row (already in rows).

	var scheduleAdv float64
	for _, k := range keys {
		day := dayTypeForSlotDate(k.date)
		price, adv, ok := matchSlotPrice(rows, k.court, day)
		if !ok {
			price = 500
			adv = 100
		}
		total += price
		scheduleAdv += adv
	}

	advancePay = scheduleAdv
	if sportAdvance > 0 {
		advancePay = sportAdvance * float64(len(keys))
	}
	if advancePay <= 0 || advancePay >= total {
		advancePay = 0
	}
	return total, advancePay, sportAdvance
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func checkoutPayAmount(mode string, total, advancePay float64) (float64, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "spot", "free":
		return 0, nil
	case "advance":
		if advancePay <= 0 {
			return 0, fmt.Errorf("Advance payment is not available for these slots.")
		}
		return advancePay, nil
	case "full", "":
		if total <= 0 {
			return 0, fmt.Errorf("Invalid booking amount.")
		}
		return total, nil
	default:
		if total <= 0 {
			return 0, fmt.Errorf("Invalid booking amount.")
		}
		return total, nil
	}
}
