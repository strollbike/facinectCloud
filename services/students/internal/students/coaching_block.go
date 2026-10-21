package students

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// syncCoachingActiveBlock mirrors a coaching batch into Bookings → Active blocks.
// Reason is stored as "Coaching: {batch name}" so the Blocks list shows category Coaching.
func (s *Service) syncCoachingActiveBlock(ctx context.Context, facilityID int64, oldName string, row *PlanRow) {
	if facilityID <= 0 || row == nil {
		return
	}
	if err := s.upsertCoachingBlock(ctx, facilityID, oldName, row); err != nil {
		log.Printf("coaching active block facility=%d plan=%q: %v", facilityID, row.PlanName, err)
	}
}

// SyncAllCoachingBlocks pushes every active batch into Active blocks and
// drops blocks for batches that were hidden.
func (s *Service) SyncAllCoachingBlocks(ctx context.Context) error {
	rows, err := s.DB.QueryContext(ctx, `
SELECT facility_id, id, name,
       COALESCE(start_time,''), COALESCE(end_time,''), COALESCE(coaching_days,''), status
FROM student_coaching_plans`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var facilityID, id int64
		var name, start, end, days, status string
		if err := rows.Scan(&facilityID, &id, &name, &start, &end, &days, &status); err != nil {
			return err
		}
		row := &PlanRow{
			PlanID: id, PlanName: name, StartTime: start, EndTime: end, CoachingDays: days,
		}
		if strings.EqualFold(status, "active") {
			if err := s.upsertCoachingBlock(ctx, facilityID, name, row); err != nil {
				log.Printf("coaching active block facility=%d plan=%q: %v", facilityID, name, err)
			}
			continue
		}
		s.removeCoachingActiveBlock(ctx, facilityID, name)
	}
	return rows.Err()
}

func (s *Service) removeCoachingActiveBlock(ctx context.Context, facilityID int64, planName string) {
	name := strings.TrimSpace(planName)
	if facilityID <= 0 || name == "" {
		return
	}
	reason := coachingBlockReason(name)
	if _, err := s.DB.ExecContext(ctx, `
UPDATE booking_recurring_blocks SET active=FALSE
WHERE facility_id=$1 AND reason=$2 AND active=TRUE`, facilityID, reason); err != nil {
		log.Printf("coaching block deactivate facility=%d plan=%q: %v", facilityID, name, err)
	}
	s.freeCoachingBlockSlots(ctx, facilityID, reason, "", "", "", 0, nil)
}

func (s *Service) upsertCoachingBlock(ctx context.Context, facilityID int64, oldName string, row *PlanRow) error {
	name := strings.TrimSpace(row.PlanName)
	if name == "" {
		name = strings.TrimSpace(oldName)
	}
	reason := coachingBlockReason(name)
	oldReason := coachingBlockReason(strings.TrimSpace(oldName))
	startM, okS := parseHHMM(row.StartTime)
	endM, okE := parseHHMM(row.EndTime)
	weekdays := parseCoachingDayList(row.CoachingDays)
	if name == "" || !okS || !okE || endM <= startM || len(weekdays) == 0 {
		if strings.TrimSpace(oldName) != "" {
			s.removeCoachingActiveBlock(ctx, facilityID, oldName)
		}
		if name != "" && !strings.EqualFold(name, oldName) {
			s.removeCoachingActiveBlock(ctx, facilityID, name)
		}
		return nil
	}
	start := fmt.Sprintf("%02d:%02d:00", startM/60, startM%60)
	end := fmt.Sprintf("%02d:%02d:00", endM/60, endM%60)
	wd := pgIntArray(weekdays)

	ids, err := s.coachingBlockRuleIDs(ctx, facilityID, reason, oldReason)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		if _, err := s.DB.ExecContext(ctx, `
INSERT INTO booking_recurring_blocks (facility_id, court_id, weekdays, start_time, end_time, reason, active)
VALUES ($1, NULL, $2::int[], $3::time, $4::time, $5, TRUE)`,
			facilityID, wd, start, end, reason); err != nil {
			return err
		}
	} else {
		if _, err := s.DB.ExecContext(ctx, `
UPDATE booking_recurring_blocks
SET court_id=NULL, weekdays=$2::int[], start_time=$3::time, end_time=$4::time,
    reason=$5, active=TRUE
WHERE id=$1`, ids[0], wd, start, end, reason); err != nil {
			return err
		}
		for _, extra := range ids[1:] {
			_, _ = s.DB.ExecContext(ctx, `
UPDATE booking_recurring_blocks SET active=FALSE WHERE id=$1`, extra)
		}
	}

	// Drop slots that belonged to this batch but no longer match the new days/time,
	// including dates already marked as class leave.
	s.freeCoachingBlockSlots(ctx, facilityID, reason, oldReason, start, end, row.PlanID, weekdays)

	mins := s.slotMinutes(ctx, facilityID)
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		loc = time.FixedZone("IST", 5*3600+30*60)
	}
	now := time.Now().In(loc)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	want := map[int]bool{}
	for _, d := range weekdays {
		want[d] = true
	}
	for i := 0; i < 30; i++ {
		day := from.AddDate(0, 0, i)
		wdNum := int(day.Weekday())
		if wdNum == 0 {
			wdNum = 7
		}
		if !want[wdNum] {
			continue
		}
		date := day.Format("2006-01-02")
		if s.planHasLeave(ctx, row.PlanID, date) {
			continue
		}
		if _, err := s.DB.ExecContext(ctx, `
INSERT INTO booking_blocks (facility_id, court_id, block_date, start_time, end_time, reason)
SELECT $1, NULL, $2::date, $3::time, $4::time, $5
WHERE NOT EXISTS (
  SELECT 1 FROM booking_blocks
  WHERE facility_id=$1 AND court_id IS NULL AND block_date=$2::date
    AND start_time=$3::time AND end_time=$4::time AND reason=$5
)`, facilityID, date, start, end, reason); err != nil {
			return err
		}
		if err := s.blockCoachingDay(ctx, facilityID, date, startM, endM, mins, reason); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) coachingBlockRuleIDs(ctx context.Context, facilityID int64, reason, oldReason string) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id FROM booking_recurring_blocks
WHERE facility_id=$1 AND (reason=$2 OR reason=$3)
ORDER BY active DESC, id`, facilityID, reason, oldReason)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Service) freeCoachingBlockSlots(
	ctx context.Context,
	facilityID int64,
	reason, oldReason, start, end string,
	planID int64,
	keepWeekdays []int,
) {
	reasons := []string{}
	for _, r := range []string{reason, oldReason} {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		dup := false
		for _, have := range reasons {
			if have == r {
				dup = true
				break
			}
		}
		if !dup {
			reasons = append(reasons, r)
		}
	}
	if len(reasons) == 0 {
		return
	}
	for _, r := range reasons {
		q := `
WITH freed AS (
  UPDATE booking_slots SET status='available', notes=NULL, updated_at=NOW()
  WHERE facility_id=$1 AND status='blocked' AND notes=$2 AND slot_date >= CURRENT_DATE`
		args := []interface{}{facilityID, r}
		if start != "" && end != "" && len(keepWeekdays) > 0 && r == reason {
			q += `
    AND NOT (
      start_time >= $3::time AND end_time <= $4::time
      AND EXTRACT(ISODOW FROM slot_date)::int = ANY($5::int[])
      AND NOT EXISTS (
        SELECT 1 FROM coaching_leaves l
        WHERE l.plan_id=$6 AND l.leave_date = booking_slots.slot_date
      )
    )`
			args = append(args, start, end, pgIntArray(keepWeekdays), planID)
		}
		q += `
  RETURNING court_id, slot_date, start_time, end_time
)
UPDATE app_slots a SET status='available'
FROM freed f
JOIN booking_courts c ON c.id = f.court_id
WHERE a.facility_id=$1 AND a.court_name=c.name AND a.slot_date=f.slot_date
  AND a.time_range = to_char(f.start_time, 'HH24:MI') || '-' || to_char(f.end_time, 'HH24:MI')
  AND lower(a.status)='blocked'`
		if _, err := s.DB.ExecContext(ctx, q, args...); err != nil {
			log.Printf("coaching block free facility=%d: %v", facilityID, err)
		}
		if r != reason || start == "" || end == "" || len(keepWeekdays) == 0 {
			_, _ = s.DB.ExecContext(ctx, `
DELETE FROM booking_blocks
WHERE facility_id=$1 AND block_date >= CURRENT_DATE AND reason=$2`, facilityID, r)
			continue
		}
		_, _ = s.DB.ExecContext(ctx, `
DELETE FROM booking_blocks
WHERE facility_id=$1 AND block_date >= CURRENT_DATE AND reason=$2
  AND (
    start_time <> $3::time OR end_time <> $4::time
    OR EXTRACT(ISODOW FROM block_date)::int <> ALL($5::int[])
    OR EXISTS (
      SELECT 1 FROM coaching_leaves l
      WHERE l.plan_id=$6 AND l.leave_date = booking_blocks.block_date
    )
  )`, facilityID, r, start, end, pgIntArray(keepWeekdays), planID)
	}
}

func (s *Service) blockCoachingDay(ctx context.Context, facilityID int64, date string, startM, endM, mins int, reason string) error {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, name, COALESCE(sport_id,0)
FROM booking_courts
WHERE facility_id=$1 AND status='active'
ORDER BY id`, facilityID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type court struct {
		id    int64
		name  string
		sport int64
	}
	var courts []court
	for rows.Next() {
		var c court
		if err := rows.Scan(&c.id, &c.name, &c.sport); err != nil {
			return err
		}
		courts = append(courts, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range courts {
		for t := startM; t+mins <= endM; t += mins {
			st := fmt.Sprintf("%02d:%02d:00", t/60, t%60)
			en := fmt.Sprintf("%02d:%02d:00", (t+mins)/60, (t+mins)%60)
			if _, err := s.DB.ExecContext(ctx, `
INSERT INTO booking_slots (facility_id, court_id, slot_date, start_time, end_time, status, notes)
VALUES ($1,$2,$3::date,$4::time,$5::time,'blocked',$6)
ON CONFLICT (court_id, slot_date, start_time) DO UPDATE
SET status='blocked', notes=EXCLUDED.notes, updated_at=NOW()
WHERE booking_slots.status IN ('available','blocked')`,
				facilityID, c.id, date, st, en, reason); err != nil {
				return err
			}
			label := fmt.Sprintf("%02d:%02d-%02d:%02d", t/60, t%60, (t+mins)/60, (t+mins)%60)
			if _, err := s.DB.ExecContext(ctx, `
INSERT INTO app_slots (facility_id, sport_id, court_name, slot_date, time_range, status)
VALUES ($1,$2,$3,$4::date,$5,'blocked')
ON CONFLICT (facility_id, sport_id, court_name, slot_date, time_range) DO UPDATE
SET status='blocked'
WHERE lower(app_slots.status) NOT IN ('booked','onhold','held')`,
				facilityID, c.sport, c.name, date, label); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) slotMinutes(ctx context.Context, facilityID int64) int {
	var mins int
	err := s.DB.QueryRowContext(ctx, `
SELECT slot_minutes FROM booking_facility_schedule WHERE facility_id=$1`, facilityID).Scan(&mins)
	if err != nil || mins < 15 || mins > 180 {
		return 60
	}
	return mins
}

func (s *Service) planHasLeave(ctx context.Context, planID int64, date string) bool {
	if planID <= 0 {
		return false
	}
	var one int
	err := s.DB.QueryRowContext(ctx, `
SELECT 1 FROM coaching_leaves WHERE plan_id=$1 AND leave_date=$2::date`, planID, date).Scan(&one)
	return err == nil
}

func coachingBlockReason(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return "Coaching: " + name
}

func parseCoachingDayList(raw string) []int {
	var out []int
	seen := map[int]bool{}
	for _, part := range strings.Split(raw, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 1 || n > 7 || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

func pgIntArray(days []int) string {
	parts := make([]string, len(days))
	for i, d := range days {
		parts[i] = strconv.Itoa(d)
	}
	return "{" + strings.Join(parts, ",") + "}"
}
