package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type courtSchedule struct {
	CourtName    string
	OpenMinutes  int
	CloseMinutes int
	SlotMinutes  int
}

func dayTypeForDate(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return "weekday"
	}
	switch t.Weekday() {
	case time.Saturday, time.Sunday:
		return "weekend"
	default:
		return "weekday"
	}
}

func parseHHMMToMinutes(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "<nil>" {
		return 0, false
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 {
		return 0, false
	}
	var hh, mm int
	if _, err := fmt.Sscanf(parts[0]+":"+parts[1], "%d:%d", &hh, &mm); err != nil {
		return 0, false
	}
	if hh == 24 && mm == 0 {
		return 24 * 60, true
	}
	if hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}

func minutesToHHMM(m int) string {
	if m < 0 {
		m = 0
	}
	if m > 24*60 {
		m = 24 * 60
	}
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

func rangesFromSchedule(openM, closeM, slotMins int) []string {
	if slotMins < 15 {
		slotMins = 60
	}
	if openM >= closeM {
		return nil
	}
	out := []string{}
	for t := openM; t+slotMins <= closeM; t += slotMins {
		out = append(out, minutesToHHMM(t)+"-"+minutesToHHMM(t+slotMins))
	}
	return out
}

func (s *Service) loadCourtSchedules(ctx context.Context, facilityID, sportID int64, date string) []courtSchedule {
	dayType := dayTypeForDate(date)
	out := []courtSchedule{}

	// 1) Explicit staff schedules (prefer day_type match, then 'all').
	rows, err := s.DB.QueryContext(ctx, `
SELECT court_name, open_time::text, close_time::text, slot_minutes, day_type
FROM facility_sport_schedules
WHERE facility_id=$1 AND status='active'
  AND (sport_id=0 OR sport_id=$2)
  AND day_type IN ('all', $3)
ORDER BY
  CASE WHEN sport_id=$2 THEN 0 ELSE 1 END,
  CASE WHEN day_type=$3 THEN 0 ELSE 1 END,
  CASE WHEN court_name='' THEN 1 ELSE 0 END,
  court_name`, facilityID, sportID, dayType)
	if err == nil {
		defer rows.Close()
		type rawSched struct {
			court, open, close, dtype string
			mins                      int
		}
		raws := []rawSched{}
		for rows.Next() {
			var r rawSched
			_ = rows.Scan(&r.court, &r.open, &r.close, &r.mins, &r.dtype)
			raws = append(raws, r)
		}
		// Collapse: court-specific overrides blank court; weekday/weekend overrides all.
		byCourt := map[string]rawSched{}
		for _, r := range raws {
			key := strings.TrimSpace(r.court)
			prev, ok := byCourt[key]
			if !ok {
				byCourt[key] = r
				continue
			}
			// Prefer exact day_type over 'all'
			if prev.dtype == "all" && r.dtype == dayType {
				byCourt[key] = r
			}
		}
		for _, r := range byCourt {
			openM, ok1 := parseHHMMToMinutes(r.open)
			closeM, ok2 := parseHHMMToMinutes(r.close)
			if !ok1 || !ok2 || openM >= closeM {
				continue
			}
			mins := r.mins
			if mins < 15 {
				mins = 60
			}
			out = append(out, courtSchedule{
				CourtName: strings.TrimSpace(r.court), OpenMinutes: openM, CloseMinutes: closeM, SlotMinutes: mins,
			})
		}
	}

	// Expand blank court_name using price-list / facility courts names.
	expanded := []courtSchedule{}
	courtNames := s.courtNamesForSport(ctx, facilityID, sportID)
	for _, sc := range out {
		if sc.CourtName != "" {
			expanded = append(expanded, sc)
			continue
		}
		names := courtNames
		if len(names) == 0 {
			names = []string{"Court 1"}
		}
		for _, n := range names {
			expanded = append(expanded, courtSchedule{
				CourtName: n, OpenMinutes: sc.OpenMinutes, CloseMinutes: sc.CloseMinutes, SlotMinutes: sc.SlotMinutes,
			})
		}
	}
	if len(expanded) > 0 {
		return dedupeCourtSchedules(expanded)
	}

	// 2) facility_courts open/close
	rows2, err := s.DB.QueryContext(ctx, `
SELECT fc.name,
       COALESCE(NULLIF(fc.open_time,''), NULLIF(fp.open_time,''), '06:00'),
       COALESCE(NULLIF(fc.close_time,''), NULLIF(fp.close_time,''), '22:00')
FROM facility_courts fc
LEFT JOIN facility_sports fs ON fs.id = fc.sport_id
LEFT JOIN facility_profiles fp ON fp.facility_id = fc.facility_id
WHERE fc.facility_id=$1 AND fc.status='active'
  AND ($2=0 OR fs.catalog_sport_id=$2 OR fc.sport_id IS NULL)`, facilityID, sportID)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var name, open, close string
			_ = rows2.Scan(&name, &open, &close)
			openM, ok1 := parseHHMMToMinutes(open)
			closeM, ok2 := parseHHMMToMinutes(close)
			if !ok1 || !ok2 || openM >= closeM {
				continue
			}
			out = append(out, courtSchedule{CourtName: name, OpenMinutes: openM, CloseMinutes: closeM, SlotMinutes: 60})
		}
	}
	if len(out) > 0 {
		return dedupeCourtSchedules(out)
	}

	// 3) booking_courts + facility profile hours
	var openDef, closeDef string
	_ = s.DB.QueryRowContext(ctx, `
SELECT COALESCE(NULLIF(open_time,''),'06:00'), COALESCE(NULLIF(close_time,''),'22:00')
FROM facility_profiles WHERE facility_id=$1`, facilityID).Scan(&openDef, &closeDef)
	openM, ok1 := parseHHMMToMinutes(openDef)
	closeM, ok2 := parseHHMMToMinutes(closeDef)
	if !ok1 || !ok2 {
		openM, closeM = 6*60, 22*60
	}
	for _, name := range s.courtNamesForSport(ctx, facilityID, sportID) {
		out = append(out, courtSchedule{CourtName: name, OpenMinutes: openM, CloseMinutes: closeM, SlotMinutes: 60})
	}
	if len(out) == 0 {
		out = append(out, courtSchedule{CourtName: "Court 1", OpenMinutes: openM, CloseMinutes: closeM, SlotMinutes: 60})
	}
	return dedupeCourtSchedules(out)
}

func dedupeCourtSchedules(in []courtSchedule) []courtSchedule {
	seen := map[string]courtSchedule{}
	for _, sc := range in {
		key := strings.ToLower(strings.TrimSpace(sc.CourtName))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; !ok {
			seen[key] = sc
		}
	}
	out := make([]courtSchedule, 0, len(seen))
	for _, sc := range seen {
		out = append(out, sc)
	}
	return out
}

func (s *Service) courtNamesForSport(ctx context.Context, facilityID, sportID int64) []string {
	names := []string{}
	seen := map[string]struct{}{}
	add := func(n string) {
		n = strings.TrimSpace(n)
		if n == "" {
			return
		}
		k := strings.ToLower(n)
		if _, ok := seen[k]; ok {
			return
		}
		seen[k] = struct{}{}
		names = append(names, n)
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT DISTINCT court_name FROM app_price_list WHERE facility_id=$1 AND ($2=0 OR sport_id=$2)`, facilityID, sportID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var n string
			_ = rows.Scan(&n)
			add(n)
		}
	}
	rows2, err := s.DB.QueryContext(ctx, `
SELECT DISTINCT name FROM booking_courts
WHERE facility_id=$1 AND status='active' AND ($2=0 OR sport_id=$2 OR sport_id IS NULL)`, facilityID, sportID)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var n string
			_ = rows2.Scan(&n)
			add(n)
		}
	}
	rows3, err := s.DB.QueryContext(ctx, `
SELECT DISTINCT fc.name FROM facility_courts fc
LEFT JOIN facility_sports fs ON fs.id = fc.sport_id
WHERE fc.facility_id=$1 AND fc.status='active'
  AND ($2=0 OR fs.catalog_sport_id=$2 OR fc.sport_id IS NULL)`, facilityID, sportID)
	if err == nil {
		defer rows3.Close()
		for rows3.Next() {
			var n string
			_ = rows3.Scan(&n)
			add(n)
		}
	}
	return names
}

// ensureSlotsForDay materializes slots from staff schedules (facility_sport_schedules /
// facility_courts / booking_courts), refreshing available rows to match open/close.
func (s *Service) ensureSlotsForDay(ctx context.Context, facilityID, sportID int64, date string) {
	schedules := s.loadCourtSchedules(ctx, facilityID, sportID, date)
	if len(schedules) == 0 {
		return
	}

	// Drop only available rows so booked/held stay intact, then re-insert from schedule.
	_, _ = s.DB.ExecContext(ctx, `
DELETE FROM app_slots
WHERE facility_id=$1 AND sport_id=$2 AND slot_date=$3::date
  AND LOWER(status)='available'`, facilityID, sportID, date)

	for _, sc := range schedules {
		for _, tr := range rangesFromSchedule(sc.OpenMinutes, sc.CloseMinutes, sc.SlotMinutes) {
			_, _ = s.DB.ExecContext(ctx, `
INSERT INTO app_slots (facility_id, sport_id, court_name, slot_date, time_range, status)
VALUES ($1,$2,$3,$4::date,$5,'available')
ON CONFLICT (facility_id, sport_id, court_name, slot_date, time_range) DO NOTHING`,
				facilityID, sportID, sc.CourtName, date, tr)
		}
	}

	// Overlay booking_slots booked/blocked when staff booking service already has inventory.
	bRows, err := s.DB.QueryContext(ctx, `
SELECT c.name, to_char(s.start_time,'HH24:MI')||'-'||to_char(s.end_time,'HH24:MI'), s.status
FROM booking_slots s
JOIN booking_courts c ON c.id = s.court_id
WHERE s.facility_id=$1 AND s.slot_date=$2::date
  AND ($3=0 OR c.sport_id=$3 OR c.sport_id IS NULL)
  AND s.status IN ('booked','blocked')`, facilityID, date, sportID)
	if err == nil {
		defer bRows.Close()
		for bRows.Next() {
			var court, tr, st string
			_ = bRows.Scan(&court, &tr, &st)
			_, _ = s.DB.ExecContext(ctx, `
INSERT INTO app_slots (facility_id, sport_id, court_name, slot_date, time_range, status)
VALUES ($1,$2,$3,$4::date,$5,$6)
ON CONFLICT (facility_id, sport_id, court_name, slot_date, time_range)
DO UPDATE SET status=EXCLUDED.status`, facilityID, sportID, court, date, tr, st)
		}
	}

	// Re-apply consumer app bookings.
	aRows, err := s.DB.QueryContext(ctx, `
SELECT slots_json FROM app_bookings
WHERE facility_id=$1 AND status='confirmed'
  AND (slots_json::text ILIKE '%'||$2||'%' OR formatted_date=$2)`, facilityID, date)
	if err != nil {
		return
	}
	defer aRows.Close()
	for aRows.Next() {
		var raw []byte
		if err := aRows.Scan(&raw); err != nil {
			continue
		}
		var slots []any
		if json.Unmarshal(raw, &slots) != nil {
			continue
		}
		for _, sl := range slots {
			m, _ := sl.(map[string]any)
			if m == nil {
				continue
			}
			court := firstNonEmpty(fmt.Sprint(m["court"]), fmt.Sprint(m["courtName"]), fmt.Sprint(m["subFacilityName"]))
			tr := firstNonEmpty(fmt.Sprint(m["time"]), fmt.Sprint(m["timeRange"]), fmt.Sprint(m["time_range"]))
			d := firstNonEmpty(fmt.Sprint(m["date"]), fmt.Sprint(m["slotDate"]), date)
			if court == "" || tr == "" || d != date {
				continue
			}
			_, _ = s.DB.ExecContext(ctx, `
UPDATE app_slots SET status='booked'
WHERE facility_id=$1 AND sport_id=$2 AND court_name=$3 AND slot_date=$4::date AND time_range=$5`,
				facilityID, sportID, court, date, tr)
		}
	}
}
