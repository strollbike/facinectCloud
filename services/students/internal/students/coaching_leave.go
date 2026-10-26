package students

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type SetCoachingLeaveInput struct {
	Date       string
	Note       string
	PlanName   string
	AllBatches bool
	CreatedBy  *int64
}

type CoachingLeaveResult struct {
	OK              bool     `json:"ok"`
	Date            string   `json:"date"`
	Note            string   `json:"note"`
	Batches         []string `json:"batches"`
	StudentsMarked  int      `json:"students_marked"`
	SlotsOpened     int      `json:"slots_opened"`
	Notified        int      `json:"notified"`
	Message         string   `json:"message"`
}

type leavePlan struct {
	ID           int64
	Name         string
	Start        string
	End          string
	CoachingDays string
}

func (s *Service) SetCoachingLeave(ctx context.Context, facilityID int64, in SetCoachingLeaveInput) (*CoachingLeaveResult, error) {
	if facilityID <= 0 {
		return nil, fmt.Errorf("invalid_facility")
	}
	date := strings.TrimSpace(in.Date)
	note := strings.TrimSpace(in.Note)
	if note == "" {
		return nil, fmt.Errorf("note_required")
	}
	if len(note) > 500 {
		note = note[:500]
	}
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		loc = time.FixedZone("IST", 5*3600+30*60)
	}
	day, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return nil, fmt.Errorf("invalid_date")
	}
	today := time.Now().In(loc)
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
	if day.Before(today) {
		return nil, fmt.Errorf("date_in_past")
	}
	weekday := int(day.Weekday())
	if weekday == 0 {
		weekday = 7
	}

	plans, err := s.activeLeavePlans(ctx, facilityID)
	if err != nil {
		return nil, err
	}
	var chosen []leavePlan
	if in.AllBatches {
		for _, p := range plans {
			if coachesOnWeekday(p.CoachingDays, weekday) {
				chosen = append(chosen, p)
			}
		}
	} else {
		name := strings.TrimSpace(in.PlanName)
		if name == "" {
			return nil, fmt.Errorf("plan_required")
		}
		var found *leavePlan
		for i := range plans {
			if strings.EqualFold(strings.TrimSpace(plans[i].Name), name) {
				found = &plans[i]
				break
			}
		}
		if found == nil {
			return nil, fmt.Errorf("plan_not_found")
		}
		if !coachesOnWeekday(found.CoachingDays, weekday) {
			return nil, fmt.Errorf("not_a_coaching_day")
		}
		chosen = append(chosen, *found)
	}
	if len(chosen) == 0 {
		return nil, fmt.Errorf("no_coaching_on_date")
	}

	var createdBy interface{}
	if in.CreatedBy != nil && *in.CreatedBy > 0 {
		createdBy = *in.CreatedBy
	}

	names := make([]string, 0, len(chosen))
	marked := 0
	opened := 0
	for _, p := range chosen {
		if _, err := s.DB.ExecContext(ctx, `
INSERT INTO coaching_leaves (facility_id, plan_id, leave_date, note, created_by)
VALUES ($1,$2,$3::date,$4,$5)
ON CONFLICT (facility_id, plan_id, leave_date) DO UPDATE
SET note=EXCLUDED.note, created_by=EXCLUDED.created_by, updated_at=NOW()`,
			facilityID, p.ID, date, note, createdBy); err != nil {
			return nil, err
		}
		n, err := s.markBatchLeave(ctx, facilityID, p.Name, date, note)
		if err != nil {
			return nil, err
		}
		marked += n
		if p.Start != "" && p.End != "" {
			slots, err := s.openBookingWindow(ctx, facilityID, date, p.Start, p.End)
			if err != nil {
				return nil, err
			}
			opened += slots
		}
		names = append(names, p.Name)
	}

	notified := s.notifyLeaveStudents(ctx, facilityID, names, date, note, false)
	msg := fmt.Sprintf(
		"Leave saved for %s. %d student(s) marked leave. %d slot(s) opened for booking. %d notification(s) queued.",
		date, marked, opened, notified,
	)
	return &CoachingLeaveResult{
		OK:             true,
		Date:           date,
		Note:           note,
		Batches:        names,
		StudentsMarked: marked,
		SlotsOpened:    opened,
		Notified:       notified,
		Message:        msg,
	}, nil
}

func (s *Service) activeLeavePlans(ctx context.Context, facilityID int64) ([]leavePlan, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, name, COALESCE(start_time,''), COALESCE(end_time,''), COALESCE(coaching_days,'')
FROM student_coaching_plans
WHERE facility_id=$1 AND status='active'
ORDER BY name`, facilityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []leavePlan
	for rows.Next() {
		var p leavePlan
		if err := rows.Scan(&p.ID, &p.Name, &p.Start, &p.End, &p.CoachingDays); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func coachesOnWeekday(coachingDays string, weekday int) bool {
	raw := strings.TrimSpace(coachingDays)
	if raw == "" {
		return true
	}
	for _, part := range strings.Split(raw, ",") {
		n := 0
		fmt.Sscanf(strings.TrimSpace(part), "%d", &n)
		if n == weekday {
			return true
		}
	}
	return false
}

func (s *Service) markBatchLeave(ctx context.Context, facilityID int64, planName, date, note string) (int, error) {
	res, err := s.DB.ExecContext(ctx, `
INSERT INTO student_attendance (facility_id, enrollment_id, attendance_date, status, notes, leave_reason)
SELECT e.facility_id, e.id, $3::date, 'leave', $4, $4
FROM student_enrollments e
WHERE e.facility_id=$1
  AND e.status='active'
  AND lower(trim(COALESCE(e.plan_name,''))) = lower(trim($2))
ON CONFLICT (enrollment_id, attendance_date) DO UPDATE
SET status='leave',
    notes=EXCLUDED.notes,
    leave_reason=EXCLUDED.leave_reason,
    updated_at=NOW()`,
		facilityID, planName, date, note)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Service) openBookingWindow(ctx context.Context, facilityID int64, date, start, end string) (int, error) {
	startM, ok1 := parseHHMM(start)
	endM, ok2 := parseHHMM(end)
	if !ok1 || !ok2 || endM <= startM {
		return 0, nil
	}
	mins := 60
	var slotMins int
	err := s.DB.QueryRowContext(ctx, `
SELECT slot_minutes FROM booking_facility_schedule WHERE facility_id=$1`, facilityID).Scan(&slotMins)
	if err == nil && slotMins >= 15 && slotMins <= 180 {
		mins = slotMins
	}

	rows, err := s.DB.QueryContext(ctx, `
SELECT id, name, COALESCE(sport_id,0)
FROM booking_courts
WHERE facility_id=$1 AND status='active'
ORDER BY id`, facilityID)
	if err != nil {
		return 0, err
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
			return 0, err
		}
		courts = append(courts, c)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	opened := 0
	for _, c := range courts {
		for t := startM; t+mins <= endM; t += mins {
			st := fmt.Sprintf("%02d:%02d:00", t/60, t%60)
			en := fmt.Sprintf("%02d:%02d:00", (t+mins)/60, (t+mins)%60)
			res, err := s.DB.ExecContext(ctx, `
INSERT INTO booking_slots (facility_id, court_id, slot_date, start_time, end_time, status)
VALUES ($1,$2,$3::date,$4::time,$5::time,'available')
ON CONFLICT (court_id, slot_date, start_time) DO UPDATE
SET status='available', notes=NULL, updated_at=NOW()
WHERE booking_slots.status IN ('blocked','available')`,
				facilityID, c.id, date, st, en)
			if err != nil {
				return opened, err
			}
			n, _ := res.RowsAffected()
			opened += int(n)
			rangeLabel := fmt.Sprintf("%02d:%02d-%02d:%02d", t/60, t%60, (t+mins)/60, (t+mins)%60)
			if _, err := s.DB.ExecContext(ctx, `
INSERT INTO app_slots (facility_id, sport_id, court_name, slot_date, time_range, status)
VALUES ($1,$2,$3,$4::date,$5,'available')
ON CONFLICT (facility_id, sport_id, court_name, slot_date, time_range) DO UPDATE
SET status='available'
WHERE lower(app_slots.status) NOT IN ('booked','onhold','held')`,
				facilityID, c.sport, c.name, date, rangeLabel); err != nil {
				return opened, err
			}
		}
	}
	return opened, nil
}

func parseHHMM(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 5 && raw[2] == ':' {
		var h, m int
		if _, err := fmt.Sscanf(raw[:5], "%d:%d", &h, &m); err == nil && h >= 0 && h < 24 && m >= 0 && m < 60 {
			return h*60 + m, true
		}
	}
	return 0, false
}

type CoachingLeaveItem struct {
	Date     string `json:"date"`
	PlanName string `json:"plan_name"`
	Note     string `json:"note"`
}

func (s *Service) ListCoachingLeaves(ctx context.Context, facilityID int64, planName string) ([]CoachingLeaveItem, error) {
	if facilityID <= 0 {
		return nil, fmt.Errorf("invalid_facility")
	}
	q := `
SELECT l.leave_date::text, p.name, COALESCE(l.note,'')
FROM coaching_leaves l
JOIN student_coaching_plans p ON p.id = l.plan_id
WHERE l.facility_id=$1 AND l.leave_date >= CURRENT_DATE AND p.status='active'`
	args := []interface{}{facilityID}
	if name := strings.TrimSpace(planName); name != "" {
		q += ` AND lower(p.name)=lower($2)`
		args = append(args, name)
	}
	q += ` ORDER BY l.leave_date, p.name`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CoachingLeaveItem{}
	for rows.Next() {
		var item CoachingLeaveItem
		if err := rows.Scan(&item.Date, &item.PlanName, &item.Note); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Service) CancelCoachingLeave(ctx context.Context, facilityID int64, in SetCoachingLeaveInput) (*CoachingLeaveResult, error) {
	if facilityID <= 0 {
		return nil, fmt.Errorf("invalid_facility")
	}
	date := strings.TrimSpace(in.Date)
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		loc = time.FixedZone("IST", 5*3600+30*60)
	}
	day, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return nil, fmt.Errorf("invalid_date")
	}
	today := time.Now().In(loc)
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
	if day.Before(today) {
		return nil, fmt.Errorf("date_in_past")
	}

	plans, err := s.activeLeavePlans(ctx, facilityID)
	if err != nil {
		return nil, err
	}
	var chosen []leavePlan
	if in.AllBatches {
		for _, p := range plans {
			if s.planHasLeave(ctx, p.ID, date) {
				chosen = append(chosen, p)
			}
		}
	} else {
		name := strings.TrimSpace(in.PlanName)
		if name == "" {
			return nil, fmt.Errorf("plan_required")
		}
		var found *leavePlan
		for i := range plans {
			if strings.EqualFold(strings.TrimSpace(plans[i].Name), name) {
				found = &plans[i]
				break
			}
		}
		if found == nil {
			return nil, fmt.Errorf("plan_not_found")
		}
		if !s.planHasLeave(ctx, found.ID, date) {
			return nil, fmt.Errorf("leave_not_found")
		}
		chosen = append(chosen, *found)
	}
	if len(chosen) == 0 {
		return nil, fmt.Errorf("leave_not_found")
	}

	names := make([]string, 0, len(chosen))
	cleared := 0
	blocked := 0
	mins := s.slotMinutes(ctx, facilityID)
	for _, p := range chosen {
		res, err := s.DB.ExecContext(ctx, `
DELETE FROM coaching_leaves
WHERE facility_id=$1 AND plan_id=$2 AND leave_date=$3::date`, facilityID, p.ID, date)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		n, err := s.clearBatchLeave(ctx, facilityID, p.Name, date)
		if err != nil {
			return nil, err
		}
		cleared += n
		startM, okS := parseHHMM(p.Start)
		endM, okE := parseHHMM(p.End)
		if okS && okE && endM > startM {
			if _, err := s.DB.ExecContext(ctx, `
INSERT INTO booking_blocks (facility_id, court_id, block_date, start_time, end_time, reason)
SELECT $1, NULL, $2::date, $3::time, $4::time, $5
WHERE NOT EXISTS (
  SELECT 1 FROM booking_blocks
  WHERE facility_id=$1 AND court_id IS NULL AND block_date=$2::date
    AND start_time=$3::time AND end_time=$4::time AND reason=$5
)`, facilityID, date,
				fmt.Sprintf("%02d:%02d:00", startM/60, startM%60),
				fmt.Sprintf("%02d:%02d:00", endM/60, endM%60),
				coachingBlockReason(p.Name)); err != nil {
				return nil, err
			}
			if err := s.blockCoachingDay(ctx, facilityID, date, startM, endM, mins, coachingBlockReason(p.Name)); err != nil {
				return nil, err
			}
			blocked++
		}
		names = append(names, p.Name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("leave_not_found")
	}
	notified := s.notifyLeaveStudents(ctx, facilityID, names, date, "", true)
	msg := fmt.Sprintf(
		"Leave cancelled for %s. Coaching block restored. %d notification(s) queued.",
		date, notified,
	)
	return &CoachingLeaveResult{
		OK:             true,
		Date:           date,
		Batches:        names,
		StudentsMarked: cleared,
		SlotsOpened:    blocked,
		Notified:       notified,
		Message:        msg,
	}, nil
}

func (s *Service) clearBatchLeave(ctx context.Context, facilityID int64, planName, date string) (int, error) {
	res, err := s.DB.ExecContext(ctx, `
DELETE FROM student_attendance a
USING student_enrollments e
WHERE a.enrollment_id = e.id
  AND a.facility_id=$1
  AND a.attendance_date=$3::date
  AND a.status='leave'
  AND e.facility_id=$1
  AND e.status='active'
  AND lower(trim(COALESCE(e.plan_name,''))) = lower(trim($2))`,
		facilityID, planName, date)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Service) notifyLeaveStudents(ctx context.Context, facilityID int64, planNames []string, date, note string, cancelled bool) int {
	if len(planNames) == 0 {
		return 0
	}
	base := strings.TrimRight(strings.TrimSpace(s.Cfg.NotificationsURL), "/")
	if base == "" {
		return 0
	}
	facilityName := s.facilityDisplayName(ctx, facilityID)
	if facilityName == "" {
		facilityName = "Academy"
	}
	placeholders := make([]string, len(planNames))
	args := []interface{}{facilityID}
	for i, name := range planNames {
		args = append(args, strings.ToLower(strings.TrimSpace(name)))
		placeholders[i] = fmt.Sprintf("$%d", i+2)
	}
	q := fmt.Sprintf(`
SELECT DISTINCT
  COALESCE(NULLIF(st.whatsapp,''), NULLIF(st.contact_phone,''), ''),
  trim(both ' ' FROM trim(COALESCE(st.first_name,'')) || ' ' || trim(COALESCE(st.last_name,'')))
FROM student_enrollments e
JOIN students st ON st.id = e.student_id
WHERE e.facility_id=$1
  AND e.status='active'
  AND lower(trim(COALESCE(e.plan_name,''))) IN (%s)`, strings.Join(placeholders, ","))
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return 0
	}
	defer rows.Close()
	type rec struct{ phone, name string }
	var list []rec
	seen := map[string]bool{}
	for rows.Next() {
		var phone, name string
		if err := rows.Scan(&phone, &name); err != nil {
			continue
		}
		phone = normalizeNotifyPhone(phone)
		if phone == "" || seen[phone] {
			continue
		}
		seen[phone] = true
		if strings.TrimSpace(name) == "" {
			name = "Student"
		}
		list = append(list, rec{phone: phone, name: name})
	}
	title := "Coaching leave"
	when := formatLeaveDate(date)
	batches := strings.Join(planNames, ", ")
	// facinect_facility_dynamic_msg: {{1}} facility, {{2}} student, {{3}} message.
	waNote := fmt.Sprintf("No coaching on %s (%s). %s", when, batches, note)
	if cancelled {
		title = "Coaching is on"
		waNote = fmt.Sprintf("Coaching is back on %s (%s).", when, batches)
	}
	sent := 0
	for _, r := range list {
		payload := map[string]interface{}{
			"channel":     "both",
			"facility_id": facilityID,
			"template":    "facinect_facility_dynamic_msg",
			"to":          map[string]string{"whatsapp": r.phone},
			"data": map[string]interface{}{
				"title":       title,
				"body":        waNote,
				"message":     waNote,
				"language":    "en_US",
				"body_params": []string{facilityName, r.name, waNote},
				"type":        map[bool]string{true: "coaching_leave_cancelled", false: "coaching_leave"}[cancelled],
				// Tray only. A high-priority heads-up on this same phone
				// stops the Facility app the coach is using.
				"android_priority": "normal",
			},
		}
		raw, _ := json.Marshal(payload)
		reqCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, base+"/v1/notifications/send", bytes.NewReader(raw))
		if err != nil {
			cancel()
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if s.Cfg.NotificationsKey != "" {
			req.Header.Set("X-Service-Key", s.Cfg.NotificationsKey)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		cancel()
		if res.StatusCode < 300 {
			var job map[string]interface{}
			_ = json.Unmarshal(b, &job)
			if st, _ := job["status"].(string); !strings.EqualFold(st, "failed") {
				sent++
			}
		}
	}
	return sent
}

func formatLeaveDate(date string) string {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(date))
	if err != nil {
		return strings.TrimSpace(date)
	}
	return t.Format("Monday, 02 Jan 2006")
}
