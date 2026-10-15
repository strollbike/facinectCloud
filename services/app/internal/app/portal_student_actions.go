package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (s *Service) studentOwnsEnrollment(ctx context.Context, enrollmentID int64, last10 string) (map[string]any, bool) {
	if enrollmentID <= 0 || len(last10) < 10 {
		return nil, false
	}
	list, err := s.liveStudentMyEnrollments(ctx, last10)
	if err != nil || len(list) == 0 {
		list, err = s.appStudentEnrollmentsByUserOrWA(ctx, 0, last10)
		if err != nil {
			return nil, false
		}
	}
	for _, row := range list {
		id, _ := strconv.ParseInt(fmt.Sprint(row["enrollmentId"]), 10, 64)
		if id == enrollmentID {
			return row, true
		}
	}
	return nil, false
}

func lastMonthOptions(n int) []map[string]any {
	now := time.Now()
	out := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		t := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, -i, 0)
		ym := t.Format("2006-01")
		out = append(out, map[string]any{
			"yearMonth": ym,
			"label":     t.Format("Jan 2006"),
			"isCurrent": i == 0,
		})
	}
	return out
}

func (s *Service) studentPortalReports(ctx context.Context, body map[string]any) (bool, string, any) {
	eid, _ := parseInt64Field(body, "enrollmentId")
	if eid <= 0 {
		return false, "enrollmentId is required.", nil
	}
	last10, errMsg := s.resolvePortalLast10(ctx, body)
	if errMsg != "" {
		return false, errMsg, nil
	}
	enr, ok := s.studentOwnsEnrollment(ctx, eid, last10)
	if !ok {
		return false, "Enrollment not found for this account.", nil
	}

	months := lastMonthOptions(3)
	currentYm := time.Now().Format("2006-01")
	reports := []map[string]any{}
	for _, opt := range months {
		ym := fmt.Sprint(opt["yearMonth"])
		present, absent := s.attendanceCounts(ctx, eid, ym)
		reports = append(reports, map[string]any{
			"yearMonth":    ym,
			"label":        opt["label"],
			"presentCount": present,
			"absentCount":  absent,
			"level":        enr["level"],
			"source":       "live",
			"isCurrent":    ym == currentYm,
		})
	}
	return true, "OK", map[string]any{
		"enrollment": enr,
		"reports":    reports,
		"monthsKept": 3,
	}
}

func (s *Service) attendanceCounts(ctx context.Context, enrollmentID int64, yearMonth string) (present, absent int) {
	if s.tableExists(ctx, "student_attendance") {
		_ = s.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FILTER (WHERE status='present'),
       COUNT(*) FILTER (WHERE status IN ('absent','leave'))
FROM student_attendance
WHERE enrollment_id=$1 AND to_char(attendance_date,'YYYY-MM')=$2`, enrollmentID, yearMonth).
			Scan(&present, &absent)
		return
	}
	_ = s.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FILTER (WHERE status='present'),
       COUNT(*) FILTER (WHERE status IN ('absent','leave'))
FROM app_student_attendance
WHERE enrollment_id=$1 AND to_char(attend_date,'YYYY-MM')=$2`, enrollmentID, yearMonth).
		Scan(&present, &absent)
	return
}

func (s *Service) studentPortalMonthCalendar(ctx context.Context, body map[string]any) (bool, string, any) {
	eid, _ := parseInt64Field(body, "enrollmentId")
	if eid <= 0 {
		return false, "enrollmentId is required.", nil
	}
	last10, errMsg := s.resolvePortalLast10(ctx, body)
	if errMsg != "" {
		return false, errMsg, nil
	}
	enr, ok := s.studentOwnsEnrollment(ctx, eid, last10)
	if !ok {
		return false, "Enrollment not found for this account.", nil
	}

	months := lastMonthOptions(3)
	currentYm := time.Now().Format("2006-01")
	ym := strings.TrimSpace(fmt.Sprint(body["yearMonth"]))
	if ym == "" {
		ym = fmt.Sprint(body["report_month"])
	}
	if ym == "" || !regexpYearMonth(ym) {
		ym = currentYm
	}
	allowed := map[string]bool{}
	label := ym
	for _, opt := range months {
		k := fmt.Sprint(opt["yearMonth"])
		allowed[k] = true
		if k == ym {
			label = fmt.Sprint(opt["label"])
		}
	}
	if !allowed[ym] {
		return false, "Select one of the last 3 months only.", nil
	}

	days, present, absent := s.buildMonthDays(ctx, eid, ym)
	return true, "OK", map[string]any{
		"enrollment":      enr,
		"yearMonth":       ym,
		"label":           label,
		"isCurrentMonth":  ym == currentYm,
		"presentCount":    present,
		"absentCount":     absent,
		"days":            days,
		"availableMonths": months,
		"monthsKept":      3,
	}
}

func regexpYearMonth(ym string) bool {
	if len(ym) != 7 || ym[4] != '-' {
		return false
	}
	_, err := time.Parse("2006-01", ym)
	return err == nil
}

func (s *Service) buildMonthDays(ctx context.Context, enrollmentID int64, yearMonth string) ([]map[string]any, int, int) {
	start, err := time.ParseInLocation("2006-01", yearMonth, time.Local)
	if err != nil {
		return []map[string]any{}, 0, 0
	}
	end := start.AddDate(0, 1, -1)
	today := time.Now()
	todayKey := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location())

	statusByDate := map[string]struct{ status, reason string }{}
	if s.tableExists(ctx, "student_attendance") {
		rows, qerr := s.DB.QueryContext(ctx, `
SELECT attendance_date::text, status, COALESCE(leave_reason, COALESCE(notes,''))
FROM student_attendance
WHERE enrollment_id=$1 AND attendance_date >= $2::date AND attendance_date <= $3::date`,
			enrollmentID, start.Format("2006-01-02"), end.Format("2006-01-02"))
		if qerr == nil {
			defer rows.Close()
			for rows.Next() {
				var d, st, reason string
				_ = rows.Scan(&d, &st, &reason)
				if len(d) >= 10 {
					d = d[:10]
				}
				statusByDate[d] = struct{ status, reason string }{st, reason}
			}
		}
	} else {
		rows, qerr := s.DB.QueryContext(ctx, `
SELECT attend_date::text, status, COALESCE(leave_reason,'')
FROM app_student_attendance
WHERE enrollment_id=$1 AND attend_date >= $2::date AND attend_date <= $3::date`,
			enrollmentID, start.Format("2006-01-02"), end.Format("2006-01-02"))
		if qerr == nil {
			defer rows.Close()
			for rows.Next() {
				var d, st, reason string
				_ = rows.Scan(&d, &st, &reason)
				if len(d) >= 10 {
					d = d[:10]
				}
				statusByDate[d] = struct{ status, reason string }{st, reason}
			}
		}
	}

	present, absent := 0, 0
	days := []map[string]any{}
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		st := ""
		reason := ""
		rawLeave := false
		if v, ok := statusByDate[key]; ok {
			st = v.status
			reason = v.reason
			rawLeave = strings.EqualFold(v.status, "leave")
		}
		switch strings.ToLower(st) {
		case "present":
			present++
		case "absent", "leave":
			absent++
			if strings.EqualFold(st, "leave") {
				st = "absent"
			}
		}
		dayStart := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, d.Location())
		isToday := dayStart.Equal(todayKey)
		isFuture := dayStart.After(todayKey)
		wd := int(d.Weekday())
		if wd == 0 {
			wd = 7
		}
		days = append(days, map[string]any{
			"date": key, "day": d.Day(), "weekday": wd,
			"status": st, "leaveReason": reason, "hasLeave": reason != "" || rawLeave,
			"isToday": isToday, "isFuture": isFuture,
		})
	}
	return days, present, absent
}

func (s *Service) studentPortalLeaveRequest(ctx context.Context, body map[string]any) (bool, string, any) {
	eid, _ := parseInt64Field(body, "enrollmentId")
	date := strings.TrimSpace(fmt.Sprint(body["leaveDate"]))
	if date == "" {
		date = strings.TrimSpace(fmt.Sprint(body["date"]))
	}
	reason := strings.TrimSpace(fmt.Sprint(body["reason"]))
	if reason == "" {
		reason = strings.TrimSpace(fmt.Sprint(body["leaveReason"]))
	}
	if eid <= 0 || date == "" || reason == "" {
		return false, "enrollmentId, leaveDate and reason are required.", nil
	}
	last10, errMsg := s.resolvePortalLast10(ctx, body)
	if errMsg != "" {
		return false, errMsg, nil
	}
	if _, ok := s.studentOwnsEnrollment(ctx, eid, last10); !ok {
		return false, "Enrollment not found for this account.", nil
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return false, "invalid leaveDate", nil
	}

	if s.tableExists(ctx, "student_attendance") {
		var fid int64
		_ = s.DB.QueryRowContext(ctx, `SELECT facility_id FROM student_enrollments WHERE id=$1`, eid).Scan(&fid)
		_, err := s.DB.ExecContext(ctx, `
INSERT INTO student_attendance (facility_id, enrollment_id, attendance_date, status, notes, leave_reason)
VALUES ($1,$2,$3::date,'leave',$4,$4)
ON CONFLICT (enrollment_id, attendance_date) DO UPDATE
SET status='leave', notes=EXCLUDED.notes, leave_reason=EXCLUDED.leave_reason, updated_at=NOW()`,
			fid, eid, date, reason)
		if err != nil {
			// leave_reason column may be missing on older schema
			_, err = s.DB.ExecContext(ctx, `
INSERT INTO student_attendance (facility_id, enrollment_id, attendance_date, status, notes)
VALUES ($1,$2,$3::date,'leave',$4)
ON CONFLICT (enrollment_id, attendance_date) DO UPDATE
SET status='leave', notes=EXCLUDED.notes, updated_at=NOW()`, fid, eid, date, reason)
			if err != nil {
				return false, err.Error(), nil
			}
		}
	} else {
		_, err := s.DB.ExecContext(ctx, `
INSERT INTO app_student_attendance (enrollment_id, attend_date, status, leave_reason)
VALUES ($1,$2::date,'leave',$3)
ON CONFLICT (enrollment_id, attend_date) DO UPDATE SET status='leave', leave_reason=$3`, eid, date, reason)
		if err != nil {
			return false, err.Error(), nil
		}
	}
	return true, "Leave requested", map[string]any{
		"enrollmentId": eid, "leaveDate": date, "status": "Absent", "leaveReason": reason,
	}
}

func (s *Service) studentPortalCancelLeave(ctx context.Context, body map[string]any) (bool, string, any) {
	eid, _ := parseInt64Field(body, "enrollmentId")
	date := strings.TrimSpace(fmt.Sprint(body["leaveDate"]))
	if date == "" {
		date = strings.TrimSpace(fmt.Sprint(body["date"]))
	}
	if eid <= 0 || date == "" {
		return false, "enrollmentId and leaveDate are required.", nil
	}
	last10, errMsg := s.resolvePortalLast10(ctx, body)
	if errMsg != "" {
		return false, errMsg, nil
	}
	if _, ok := s.studentOwnsEnrollment(ctx, eid, last10); !ok {
		return false, "Enrollment not found for this account.", nil
	}

	if s.tableExists(ctx, "student_attendance") {
		_, _ = s.DB.ExecContext(ctx, `
DELETE FROM student_attendance
WHERE enrollment_id=$1 AND attendance_date=$2::date AND status='leave'`, eid, date)
	} else {
		_, _ = s.DB.ExecContext(ctx, `
DELETE FROM app_student_attendance
WHERE enrollment_id=$1 AND attend_date=$2::date AND status='leave'`, eid, date)
	}
	return true, "Cancelled", map[string]any{
		"enrollmentId": eid, "leaveDate": date, "cancelled": true,
	}
}
