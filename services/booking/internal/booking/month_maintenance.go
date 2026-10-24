package booking

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type MaintenanceResult struct {
	OK               bool                 `json:"ok"`
	Message          string               `json:"message"`
	CleanedPast      int                  `json:"cleanedPast"`
	CleanedBeyond30d int                  `json:"cleanedBeyond30d"`
	ExpiredHoldSlots int                  `json:"expiredHoldSlots"`
	CleanedPrevMonth int                  `json:"cleanedPreviousMonth"`
	MonthEndRan      bool                 `json:"monthEndRan"`
	MonthEndKey      string               `json:"monthEndKey,omitempty"`
	Generate         *GenerateMonthResult `json:"generate,omitempty"`
}

// isLastDayOfMonth reports whether t is the final calendar day of its month.
func isLastDayOfMonth(t time.Time) bool {
	tomorrow := t.AddDate(0, 0, 1)
	return tomorrow.Month() != t.Month()
}

func isFirstDayOfMonth(t time.Time) bool {
	return t.Day() == 1
}

// monthEndJobKey identifies one month-end cycle (run on last day or next morning).
func monthEndJobKey(now time.Time) string {
	if isFirstDayOfMonth(now) {
		// Morning of new month → close out previous month.
		prev := now.AddDate(0, 0, -1)
		return fmt.Sprintf("month_end_%04d-%02d", prev.Year(), int(prev.Month()))
	}
	return fmt.Sprintf("month_end_%04d-%02d", now.Year(), int(now.Month()))
}

func (s *Service) ensureSlotJobsTable(ctx context.Context) {
	_, _ = s.DB.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS booking_slot_jobs (
  job_key     TEXT PRIMARY KEY,
  last_run_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  meta_json   JSONB NOT NULL DEFAULT '{}'::jsonb
)`)
}

func (s *Service) monthEndAlreadyDone(ctx context.Context, key string) bool {
	s.ensureSlotJobsTable(ctx)
	var one int
	err := s.DB.QueryRowContext(ctx,
		`SELECT 1 FROM booking_slot_jobs WHERE job_key=$1 LIMIT 1`, key,
	).Scan(&one)
	return err == nil
}

func (s *Service) markMonthEndDone(ctx context.Context, key string, meta string) {
	s.ensureSlotJobsTable(ctx)
	if meta == "" {
		meta = "{}"
	}
	_, _ = s.DB.ExecContext(ctx, `
INSERT INTO booking_slot_jobs (job_key, last_run_at, meta_json)
VALUES ($1, NOW(), $2::jsonb)
ON CONFLICT (job_key) DO UPDATE SET last_run_at=NOW(), meta_json=EXCLUDED.meta_json`,
		key, meta)
}

// CleanupBeyondMonthWindow removes Free/Blocked inventory past today+29 for all facilities.
// Booked rows are kept. Stops year-ahead generate leftovers from accumulating.
func (s *Service) CleanupBeyondMonthWindow(ctx context.Context) (int, error) {
	_, winTo := MonthWindow(time.Now())
	cutoff := winTo.Format("2006-01-02")
	res, err := s.DB.ExecContext(ctx, `
DELETE FROM booking_slots s
WHERE s.slot_date > $1::date
  AND s.status IN ('available', 'blocked')
  AND NOT EXISTS (SELECT 1 FROM booking_bookings b WHERE b.slot_id = s.id)`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	res2, err := s.DB.ExecContext(ctx, `
DELETE FROM app_slots
WHERE slot_date > $1::date
  AND LOWER(status) IN ('available', 'blocked', 'free')`, cutoff)
	if err == nil {
		n2, _ := res2.RowsAffected()
		n += n2
	}
	return int(n), nil
}

func (s *Service) slotLocation() *time.Location {
	tz := "Asia/Kolkata"
	if s != nil {
		if v := strings.TrimSpace(s.Cfg.SlotTimezone); v != "" {
			tz = v
		}
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.FixedZone("IST", 5*3600+1800)
	}
	return loc
}

// CleanupPastAvailableSlots removes past Free inventory and past Coaching blocked
// slots for all facilities. Booked rows and other blocks (Membership, Maintenance)
// are kept. The recurring Active block rule stays, so future coaching dates
// continue to be generated. Uses BOOKING_SLOT_TIMEZONE (default Asia/Kolkata).
// Also drops today's free slots from 15 minutes before end_time (Asia/Kolkata by default).
// Coaching blocks are removed only after they have actually ended.
func (s *Service) CleanupPastAvailableSlots(ctx context.Context) (int, error) {
	now := time.Now().In(s.slotLocation())
	today := now.Format("2006-01-02")
	clock := now.Format("15:04:05")
	freeCutoff := now.Add(freeSlotHideLead)
	freeClock := freeCutoff.Format("15:04:05")
	if freeCutoff.Format("2006-01-02") != today {
		// Within 15 minutes of midnight every slot ending today is inside the window.
		freeClock = "24:00:00"
	}

	res, err := s.DB.ExecContext(ctx, `
DELETE FROM booking_slots s
WHERE s.status = 'available'
  AND (
    s.slot_date < $1::date
    OR (s.slot_date = $1::date AND s.end_time <= $2::time)
  )
  AND NOT EXISTS (SELECT 1 FROM booking_bookings b WHERE b.slot_id = s.id)`,
		today, freeClock)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()

	res2, err := s.DB.ExecContext(ctx, `
DELETE FROM app_slots
WHERE LOWER(status) IN ('available', 'free')
  AND (
    slot_date < $1::date
    OR (
      slot_date = $1::date
      AND time_range ~ '^[0-9]{2}:[0-9]{2}-[0-9]{2}:[0-9]{2}$'
      AND split_part(time_range, '-', 2)::time <= $2::time
    )
  )`, today, freeClock)
	if err == nil {
		n2, _ := res2.RowsAffected()
		n += n2
	}

	// Mirror rows for past coaching blocks before the booking_slots rows go away.
	res3, err := s.DB.ExecContext(ctx, `
DELETE FROM app_slots a
USING booking_slots s
JOIN booking_courts c ON c.id = s.court_id
WHERE a.facility_id = s.facility_id
  AND a.court_name = c.name
  AND a.slot_date = s.slot_date
  AND a.time_range = to_char(s.start_time, 'HH24:MI') || '-' || to_char(s.end_time, 'HH24:MI')
  AND LOWER(a.status) = 'blocked'
  AND s.status = 'blocked'
  AND s.notes ILIKE 'Coaching:%'
  AND (
    s.slot_date < $1::date
    OR (s.slot_date = $1::date AND s.end_time <= $2::time)
  )
  AND NOT EXISTS (
    SELECT 1 FROM booking_bookings b
    WHERE b.slot_id = s.id AND b.status <> 'cancelled'
  )`, today, clock)
	if err == nil {
		n3, _ := res3.RowsAffected()
		n += n3
	}

	res4, err := s.DB.ExecContext(ctx, `
DELETE FROM booking_slots s
WHERE s.status = 'blocked'
  AND s.notes ILIKE 'Coaching:%'
  AND (
    s.slot_date < $1::date
    OR (s.slot_date = $1::date AND s.end_time <= $2::time)
  )
  AND NOT EXISTS (
    SELECT 1 FROM booking_bookings b
    WHERE b.slot_id = s.id AND b.status <> 'cancelled'
  )`, today, clock)
	if err != nil {
		return int(n), err
	}
	n4, _ := res4.RowsAffected()
	n += n4

	res5, err := s.DB.ExecContext(ctx, `
DELETE FROM booking_blocks
WHERE reason ILIKE 'Coaching:%'
  AND (
    block_date < $1::date
    OR (block_date = $1::date AND end_time <= $2::time)
  )`, today, clock)
	if err == nil {
		n5, _ := res5.RowsAffected()
		n += n5
	}
	return int(n), nil
}

// CleanupPreviousMonthSlots deletes past Free slots from months before the current calendar month.
func (s *Service) CleanupPreviousMonthSlots(ctx context.Context, now time.Time) (int, error) {
	firstOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	cutoff := firstOfMonth.Format("2006-01-02")

	res, err := s.DB.ExecContext(ctx, `
DELETE FROM booking_slots s
WHERE s.slot_date < $1::date
  AND s.status = 'available'
  AND NOT EXISTS (SELECT 1 FROM booking_bookings b WHERE b.slot_id = s.id)`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()

	res2, err := s.DB.ExecContext(ctx, `
DELETE FROM app_slots
WHERE slot_date < $1::date
  AND LOWER(status) = 'available'`, cutoff)
	if err == nil {
		n2, _ := res2.RowsAffected()
		n += n2
	}
	return int(n), nil
}

// RunSlotMaintenance: daily past cleanup + generate; on month-end (last day or 1st) also wipe previous months once.
func (s *Service) RunSlotMaintenance(ctx context.Context, now time.Time, forceMonthEnd bool) (*MaintenanceResult, error) {
	if now.IsZero() {
		now = time.Now()
	}
	out := &MaintenanceResult{OK: true}

	cleanedPast, err := s.CleanupPastAvailableSlots(ctx)
	if err != nil {
		return nil, err
	}
	out.CleanedPast = cleanedPast

	if n, err := s.ExpireDuePaymentHolds(ctx); err == nil {
		out.ExpiredHoldSlots = n
	}

	cleanedBeyond, err := s.CleanupBeyondMonthWindow(ctx)
	if err != nil {
		return nil, err
	}
	out.CleanedBeyond30d = cleanedBeyond

	shouldMonthEnd := forceMonthEnd || isLastDayOfMonth(now) || isFirstDayOfMonth(now)
	key := monthEndJobKey(now)
	if shouldMonthEnd && (forceMonthEnd || !s.monthEndAlreadyDone(ctx, key)) {
		n, err := s.CleanupPreviousMonthSlots(ctx, now)
		if err != nil {
			return nil, err
		}
		out.CleanedPrevMonth = n
		out.MonthEndRan = true
		out.MonthEndKey = key
	}

	gen, err := s.GenerateAllFacilitiesMonthWindow(ctx)
	if err != nil {
		return nil, err
	}
	out.Generate = gen

	if out.MonthEndRan {
		meta := fmt.Sprintf(`{"cleanedPreviousMonth":%d,"created":%d,"from":%q,"to":%q}`,
			out.CleanedPrevMonth, gen.Created, gen.From, gen.To)
		s.markMonthEndDone(ctx, key, meta)
	}

	out.Message = fmt.Sprintf(
		"cleanedPast=%d cleanedBeyond30d=%d expiredHoldSlots=%d cleanedPrevMonth=%d monthEnd=%v; %s",
		out.CleanedPast, out.CleanedBeyond30d, out.ExpiredHoldSlots, out.CleanedPrevMonth, out.MonthEndRan, gen.Message,
	)
	return out, nil
}
