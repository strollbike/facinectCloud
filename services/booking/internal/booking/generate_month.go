package booking

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MonthWindow is a rolling 30-day Free-slot inventory: today .. today+29 days.
// Monthly cron re-fills this window (not a full year).
func MonthWindow(now time.Time) (from, to time.Time) {
	from = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	to = from.AddDate(0, 0, 29)
	return from, to
}

type GenerateMonthResult struct {
	OK           bool    `json:"ok"`
	Message      string  `json:"message"`
	From         string  `json:"from"`
	To           string  `json:"to"`
	Facilities   int     `json:"facilities"`
	Created      int     `json:"created"`
	FacilityIDs  []int64 `json:"facilityIds"`
	SkippedEmpty int     `json:"skippedNoCourts"`
}

// useMonthWindow — PHP-style range when month_window=true or date omitted.
func (in *GenerateSlotsInput) useMonthWindow() bool {
	return in.UseMonthWindowPublic()
}

// UseMonthWindowPublic is used by HTTP handlers for response metadata.
func (in *GenerateSlotsInput) UseMonthWindowPublic() bool {
	if in == nil {
		return true
	}
	if in.MonthWindow != nil {
		return *in.MonthWindow
	}
	// No single date → fill month window (StrollX / cron).
	return strings.TrimSpace(in.Date) == "" && strings.TrimSpace(in.DateTo) == ""
}

// GenerateSlots fills one day, or a range / PHP month window.
func (s *Service) GenerateSlots(ctx context.Context, facilityID int64, in GenerateSlotsInput) (int, error) {
	if facilityID <= 0 {
		return 0, fmt.Errorf("invalid_facility")
	}

	openH := s.Cfg.DefaultOpenHour
	closeH := s.Cfg.DefaultCloseHour
	mins := s.Cfg.DefaultSlotMins
	// Prefer last manual Generate hours when caller omits open/close (cron / day fill).
	if in.OpenHour == nil || in.CloseHour == nil || in.SlotMinutes == nil {
		if so, sc, sm, ok := s.loadFacilitySchedule(ctx, facilityID); ok {
			if in.OpenHour == nil {
				openH = so
			}
			if in.CloseHour == nil {
				closeH = sc
			}
			if in.SlotMinutes == nil {
				mins = sm
			}
		}
	}
	if in.OpenHour != nil {
		openH = *in.OpenHour
	}
	if in.CloseHour != nil {
		closeH = *in.CloseHour
	}
	if in.SlotMinutes != nil {
		mins = *in.SlotMinutes
	}
	if openH < 0 || closeH > 24 || openH >= closeH || mins < 15 || mins > 240 {
		return 0, fmt.Errorf("invalid_schedule")
	}
	// Persist hours from explicit client Generate so cron keeps the same window.
	if in.OpenHour != nil && in.CloseHour != nil {
		_ = s.saveFacilitySchedule(ctx, facilityID, openH, closeH, mins)
	}

	winFrom, winTo := MonthWindow(time.Now())
	var from, to time.Time
	switch {
	case in.useMonthWindow():
		from, to = winFrom, winTo
	case strings.TrimSpace(in.DateTo) != "":
		f, err := time.Parse("2006-01-02", strings.TrimSpace(in.Date))
		if err != nil {
			return 0, fmt.Errorf("invalid_date")
		}
		t, err := time.Parse("2006-01-02", strings.TrimSpace(in.DateTo))
		if err != nil {
			return 0, fmt.Errorf("invalid_date_to")
		}
		from, to = f, t
		if to.Before(from) {
			return 0, fmt.Errorf("invalid_date_range")
		}
		// Hard cap: never invent Free inventory past the rolling 30-day window.
		if from.Before(winFrom) {
			from = winFrom
		}
		if to.After(winTo) {
			to = winTo
		}
		if to.Before(from) {
			return 0, fmt.Errorf("date_range_outside_30_day_window")
		}
	default:
		if err := validateDate(in.Date); err != nil {
			return 0, err
		}
		d, _ := time.Parse("2006-01-02", strings.TrimSpace(in.Date))
		if d.Before(winFrom) || d.After(winTo) {
			return 0, fmt.Errorf("date_outside_30_day_window")
		}
		from, to = d, d
	}

	courts, err := s.ListCourts(ctx, facilityID)
	if err != nil {
		return 0, err
	}
	if in.CourtID != nil && *in.CourtID > 0 {
		filtered := courts[:0]
		for _, c := range courts {
			if c.ID == *in.CourtID {
				filtered = append(filtered, c)
			}
		}
		courts = filtered
	}
	if len(courts) == 0 {
		return 0, fmt.Errorf("no_courts")
	}

	replace := in.Replace != nil && *in.Replace
	fromStr := from.Format("2006-01-02")
	toStr := to.Format("2006-01-02")

	if replace {
		booked, blocked, err := s.countBusySlotsInRange(ctx, facilityID, fromStr, toStr)
		if err != nil {
			return 0, err
		}
		if booked > 0 || blocked > 0 {
			return 0, fmt.Errorf(
				"conflict: %d booked and %d blocked slot(s) in %s → %s — clear them before regenerating",
				booked, blocked, fromStr, toStr,
			)
		}
		if err := s.deleteAvailableSlotsInRange(ctx, facilityID, fromStr, toStr); err != nil {
			return 0, err
		}
	}

	created := 0
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		n, err := s.generateSlotsForDay(ctx, facilityID, d, courts, openH, closeH, mins, replace)
		if err != nil {
			return created, err
		}
		created += n
	}
	// Keep Proximity app_slots in sync for the same window.
	_ = s.mirrorAppSlotsRange(ctx, facilityID, from, to)
	return created, nil
}

func (s *Service) countBusySlotsInRange(ctx context.Context, facilityID int64, from, to string) (booked, blocked int, err error) {
	err = s.DB.QueryRowContext(ctx, `
SELECT
  COUNT(*) FILTER (WHERE status = 'booked')::int,
  COUNT(*) FILTER (WHERE status IN ('blocked', 'held'))::int
FROM booking_slots
WHERE facility_id=$1
  AND slot_date BETWEEN $2::date AND $3::date
  AND status IN ('booked', 'blocked', 'held')`, facilityID, from, to).Scan(&booked, &blocked)
	return booked, blocked, err
}

func (s *Service) deleteAvailableSlotsInRange(ctx context.Context, facilityID int64, from, to string) error {
	_, err := s.DB.ExecContext(ctx, `
DELETE FROM booking_slots
WHERE facility_id=$1
  AND slot_date BETWEEN $2::date AND $3::date
  AND status = 'available'
  AND NOT EXISTS (SELECT 1 FROM booking_bookings b WHERE b.slot_id = booking_slots.id)`,
		facilityID, from, to)
	if err != nil {
		return err
	}
	_, _ = s.DB.ExecContext(ctx, `
DELETE FROM app_slots
WHERE facility_id=$1
  AND slot_date BETWEEN $2::date AND $3::date
  AND LOWER(status) = 'available'`, facilityID, from, to)
	return nil
}

func (s *Service) generateSlotsForDay(
	ctx context.Context,
	facilityID int64,
	day time.Time,
	courts []Court,
	openH, closeH, mins int,
	replace bool,
) (int, error) {
	created := 0
	date := day.Format("2006-01-02")
	conflictClause := "ON CONFLICT (court_id, slot_date, start_time) DO NOTHING"
	if replace {
		// Free rows were wiped; still safe if a race recreates one.
		conflictClause = `ON CONFLICT (court_id, slot_date, start_time) DO UPDATE
SET end_time = EXCLUDED.end_time,
    status = 'available',
    notes = NULL,
    updated_at = NOW()
WHERE booking_slots.status = 'available'`
	}
	for _, court := range courts {
		for t := openH * 60; t+mins <= closeH*60; t += mins {
			start := fmt.Sprintf("%02d:%02d:00", t/60, t%60)
			endM := t + mins
			end := fmt.Sprintf("%02d:%02d:00", endM/60, endM%60)
			res, err := s.DB.ExecContext(ctx, `
INSERT INTO booking_slots (facility_id, court_id, slot_date, start_time, end_time, status)
VALUES ($1, $2, $3::date, $4::time, $5::time, 'available')
`+conflictClause,
				facilityID, court.ID, date, start, end)
			if err != nil {
				return created, err
			}
			n, _ := res.RowsAffected()
			created += int(n)
		}
	}
	// Re-apply permanent / stored blocks onto newly created available slots.
	_, _ = s.ApplyBlocksForDate(ctx, facilityID, day)
	return created, nil
}

func (s *Service) loadFacilitySchedule(ctx context.Context, facilityID int64) (openH, closeH, mins int, ok bool) {
	err := s.DB.QueryRowContext(ctx, `
SELECT open_hour, close_hour, slot_minutes
FROM booking_facility_schedule WHERE facility_id=$1`, facilityID).
		Scan(&openH, &closeH, &mins)
	return openH, closeH, mins, err == nil
}

func (s *Service) saveFacilitySchedule(ctx context.Context, facilityID int64, openH, closeH, mins int) error {
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO booking_facility_schedule (facility_id, open_hour, close_hour, slot_minutes, updated_at)
VALUES ($1,$2,$3,$4,NOW())
ON CONFLICT (facility_id) DO UPDATE SET
  open_hour=EXCLUDED.open_hour,
  close_hour=EXCLUDED.close_hour,
  slot_minutes=EXCLUDED.slot_minutes,
  updated_at=NOW()`, facilityID, openH, closeH, mins)
	return err
}

// GenerateFacilityMonthWindow fills today → +29 days for one facility.
func (s *Service) GenerateFacilityMonthWindow(ctx context.Context, facilityID int64) (int, error) {
	mw := true
	return s.GenerateSlots(ctx, facilityID, GenerateSlotsInput{MonthWindow: &mw})
}

// GenerateAllFacilitiesMonthWindow fills the rolling 30-day window for all facilities (cron).
func (s *Service) GenerateAllFacilitiesMonthWindow(ctx context.Context) (*GenerateMonthResult, error) {
	from, to := MonthWindow(time.Now())
	out := &GenerateMonthResult{
		OK:          true,
		From:        from.Format("2006-01-02"),
		To:          to.Format("2006-01-02"),
		FacilityIDs: []int64{},
	}

	ids, err := s.listFacilitiesWithCourts(ctx)
	if err != nil {
		return nil, err
	}
	mw := true
	for _, fid := range ids {
		n, err := s.GenerateSlots(ctx, fid, GenerateSlotsInput{MonthWindow: &mw})
		if err != nil {
			if err.Error() == "no_courts" {
				out.SkippedEmpty++
				continue
			}
			// Continue other facilities; record partial success.
			continue
		}
		out.Created += n
		out.Facilities++
		out.FacilityIDs = append(out.FacilityIDs, fid)
	}
	out.Message = fmt.Sprintf(
		"Generated %d slot(s) for %d facility(ies) from %s to %s (30-day window).",
		out.Created, out.Facilities, out.From, out.To,
	)
	return out, nil
}

func (s *Service) listFacilitiesWithCourts(ctx context.Context) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT DISTINCT facility_id FROM booking_courts
WHERE status='active' AND facility_id>0
ORDER BY facility_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// mirrorAppSlotsRange copies available booking_slots into app_slots for Proximity.
func (s *Service) mirrorAppSlotsRange(ctx context.Context, facilityID int64, from, to time.Time) error {
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO app_slots (facility_id, sport_id, court_name, slot_date, time_range, status)
SELECT s.facility_id,
       COALESCE(c.sport_id, 0),
       c.name,
       s.slot_date,
       to_char(s.start_time, 'HH24:MI') || '-' || to_char(s.end_time, 'HH24:MI'),
       CASE WHEN s.status IN ('booked','blocked') THEN s.status ELSE 'available' END
FROM booking_slots s
JOIN booking_courts c ON c.id = s.court_id
WHERE s.facility_id=$1
  AND s.slot_date BETWEEN $2::date AND $3::date
ON CONFLICT (facility_id, sport_id, court_name, slot_date, time_range) DO NOTHING`,
		facilityID, from.Format("2006-01-02"), to.Format("2006-01-02"))
	return err
}
