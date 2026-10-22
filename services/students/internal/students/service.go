package students

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/facinect/students/internal/config"
)

type Service struct {
	DB  *sql.DB
	Cfg config.Config
}

type StudentRow struct {
	EnrollmentID  int64    `json:"enrollment_id"`
	StudentID     int64    `json:"student_id"`
	FacilityID    int64    `json:"facility_id"`
	FirstName     string   `json:"first_name"`
	LastName      string   `json:"last_name"`
	Email         string   `json:"contact_email,omitempty"`
	Phone         string   `json:"contact_phone,omitempty"`
	WhatsApp      string   `json:"whatsapp,omitempty"`
	PhotoURL      string   `json:"photo_url,omitempty"`
	SportID       *int64   `json:"sport_id,omitempty"`
	PlanName      string   `json:"plan_name,omitempty"`
	StartDate     string   `json:"start_date,omitempty"`
	EndDate       string   `json:"end_date,omitempty"`
	ActualFee     *float64 `json:"actual_fee,omitempty"`
	Status        string   `json:"status"`
	PaymentStatus string   `json:"payment_status"`
	UnpaidCount   int      `json:"unpaid_count"`
	CreatedAt     string   `json:"created_at,omitempty"`
}

type AttendanceRow struct {
	EnrollmentID   int64  `json:"enrollment_id"`
	StudentID      int64  `json:"student_id"`
	FacilityID     int64  `json:"facility_id"`
	FirstName      string `json:"first_name"`
	LastName       string `json:"last_name"`
	PhotoURL       string `json:"photo_url,omitempty"`
	Phone          string `json:"contact_phone,omitempty"`
	PlanName       string `json:"plan_name,omitempty"`
	AttendanceDate string `json:"attendance_date"`
	Status         string `json:"status,omitempty"`
	AttendanceID   *int64 `json:"attendance_id,omitempty"`
	Notes          string `json:"notes,omitempty"`
}

type EnrollInput struct {
	FirstName string   `json:"first_name"`
	LastName  string   `json:"last_name"`
	Email     string   `json:"contact_email"`
	Phone     string   `json:"contact_phone"`
	WhatsApp  string   `json:"whatsapp"`
	SportID   *int64   `json:"sport_id"`
	PlanName  string   `json:"plan_name"`
	StartDate string   `json:"start_date"`
	EndDate   string   `json:"end_date"`
	ActualFee *float64 `json:"actual_fee"`
}

type MarkAttendanceInput struct {
	EnrollmentID int64  `json:"enrollment_id"`
	Date         string `json:"date"`
	Status       string `json:"status"`
	Notes        string `json:"notes"`
	MarkedBy     *int64 `json:"marked_by"`
	Role         string `json:"-"` // JWT role from HTTP layer
}

type UpdateStatusInput struct {
	Status string `json:"status"`
}

type UpdateStudentInput struct {
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Email     string `json:"contact_email"`
	Phone     string `json:"contact_phone"`
	WhatsApp  string `json:"whatsapp"`
}

type UpdatePlanInput struct {
	PlanName  string   `json:"plan_name"`
	StartDate string   `json:"start_date"`
	EndDate   string   `json:"end_date"`
	ActualFee *float64 `json:"actual_fee"`
}

type PlanRow struct {
	PlanID      int64   `json:"plan_id"`
	PlanName    string  `json:"plan_name"`
	Count       int     `json:"student_count"`
	TotalFee    float64 `json:"total_fee"`
	DaysPerWeek int     `json:"days_per_week"`
	Description string  `json:"description,omitempty"`
	StartTime    string `json:"start_time,omitempty"` // HH:mm
	EndTime      string `json:"end_time,omitempty"`   // HH:mm
	CoachingDays string `json:"coaching_days,omitempty"` // "1,3,5" Mon=1 … Sun=7
}

type CreatePlanInput struct {
	PlanName    string
	TotalFee    float64
	DaysPerWeek int
	Description string
	StartTime    string // HH:mm
	EndTime      string // HH:mm
	CoachingDays string // "1,3,5"
}

// UpdateCoachingPlanInput edits a catalog batch (student_coaching_plans).
type UpdateCoachingPlanInput struct {
	PlanName    string
	TotalFee    *float64
	DaysPerWeek *int
	Description *string
	StartTime    *string
	EndTime      *string
	CoachingDays *string
}

func (s *Service) ListStudents(ctx context.Context, facilityID int64, sportID *int64, status string) ([]StudentRow, error) {
	if facilityID <= 0 {
		return nil, fmt.Errorf("invalid_facility")
	}
	q := `
SELECT e.id, s.id, e.facility_id, s.first_name, s.last_name,
       COALESCE(s.contact_email,''), COALESCE(s.contact_phone,''), COALESCE(s.whatsapp,''),
       COALESCE(s.photo_url,''),
       e.sport_id, COALESCE(e.plan_name,''),
       COALESCE(e.start_date::text,''), COALESCE(e.end_date::text,''), e.actual_fee,
       e.status,
       COALESCE((
         SELECT CASE
           WHEN EXISTS (
             SELECT 1 FROM fee_bills b
             WHERE b.facility_id = e.facility_id
               AND LOWER(COALESCE(b.bill_type,'')) = 'student'
               AND b.enrollment_id = e.id
               AND NOT (
                 LOWER(COALESCE(b.payment_status,'')) = 'paid'
                 OR (COALESCE(b.bill_amount,0) > 0 AND COALESCE(b.paid_amount,0) >= COALESCE(b.bill_amount,0))
               )
           ) THEN 'unpaid'
           WHEN EXISTS (
             SELECT 1 FROM fee_bills b
             WHERE b.facility_id = e.facility_id
               AND LOWER(COALESCE(b.bill_type,'')) = 'student'
               AND b.enrollment_id = e.id
           ) THEN 'paid'
           WHEN COALESCE(e.actual_fee,0) > 0 THEN 'unpaid'
           ELSE 'n/a'
         END
       ), 'n/a') AS payment_status,
       COALESCE((
         SELECT COUNT(*)::int FROM fee_bills b
         WHERE b.facility_id = e.facility_id
           AND LOWER(COALESCE(b.bill_type,'')) = 'student'
           AND b.enrollment_id = e.id
           AND NOT (
             LOWER(COALESCE(b.payment_status,'')) = 'paid'
             OR (COALESCE(b.bill_amount,0) > 0 AND COALESCE(b.paid_amount,0) >= COALESCE(b.bill_amount,0))
           )
       ), CASE WHEN COALESCE(e.actual_fee,0) > 0 THEN 1 ELSE 0 END) AS unpaid_count,
       e.created_at::text
FROM student_enrollments e
JOIN students s ON s.id = e.student_id
WHERE e.facility_id=$1`
	args := []interface{}{facilityID}
	argN := 2
	if sportID != nil && *sportID > 0 {
		q += fmt.Sprintf(` AND e.sport_id=$%d`, argN)
		args = append(args, *sportID)
		argN++
	}
	st := strings.TrimSpace(strings.ToLower(status))
	if st != "" && st != "all" {
		q += fmt.Sprintf(` AND e.status=$%d`, argN)
		args = append(args, st)
	}
	q += ` ORDER BY s.first_name, s.last_name, e.id DESC`

	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StudentRow{}
	for rows.Next() {
		var r StudentRow
		var sport sql.NullInt64
		var fee sql.NullFloat64
		if err := rows.Scan(
			&r.EnrollmentID, &r.StudentID, &r.FacilityID, &r.FirstName, &r.LastName,
			&r.Email, &r.Phone, &r.WhatsApp, &r.PhotoURL, &sport, &r.PlanName,
			&r.StartDate, &r.EndDate, &fee, &r.Status, &r.PaymentStatus, &r.UnpaidCount, &r.CreatedAt,
		); err != nil {
			return nil, err
		}
		if sport.Valid {
			v := sport.Int64
			r.SportID = &v
		}
		if fee.Valid {
			v := fee.Float64
			r.ActualFee = &v
		}
		if r.PaymentStatus == "" {
			r.PaymentStatus = "n/a"
		}
		if r.PaymentStatus == "unpaid" && r.UnpaidCount <= 0 {
			r.UnpaidCount = 1
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) CountActive(ctx context.Context, facilityID int64) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM student_enrollments
WHERE facility_id=$1 AND status='active'`, facilityID).Scan(&n)
	return n, err
}

func (s *Service) Enroll(ctx context.Context, facilityID int64, in EnrollInput) (*StudentRow, error) {
	first := strings.TrimSpace(in.FirstName)
	last := strings.TrimSpace(in.LastName)
	if facilityID <= 0 || first == "" {
		return nil, fmt.Errorf("invalid_student")
	}
	if in.StartDate != "" {
		if err := validateDate(in.StartDate); err != nil {
			return nil, err
		}
	}
	if in.EndDate != "" {
		if err := validateDate(in.EndDate); err != nil {
			return nil, err
		}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var studentID int64
	err = tx.QueryRowContext(ctx, `
INSERT INTO students (first_name, last_name, contact_email, contact_phone, whatsapp, status)
VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),'active')
RETURNING id`,
		first, last,
		strings.TrimSpace(in.Email),
		strings.TrimSpace(in.Phone),
		strings.TrimSpace(in.WhatsApp),
	).Scan(&studentID)
	if err != nil {
		return nil, err
	}

	var sport interface{}
	if in.SportID != nil {
		sport = *in.SportID
	}
	var start, end, fee interface{}
	if strings.TrimSpace(in.StartDate) != "" {
		start = in.StartDate
	}
	if strings.TrimSpace(in.EndDate) != "" {
		end = in.EndDate
	}
	if in.ActualFee != nil {
		fee = *in.ActualFee
	}

	var enrollmentID int64
	err = tx.QueryRowContext(ctx, `
INSERT INTO student_enrollments
  (facility_id, student_id, sport_id, plan_name, start_date, end_date, actual_fee, status)
VALUES ($1,$2,$3,NULLIF($4,''),$5::date,$6::date,$7,'active')
RETURNING id`,
		facilityID, studentID, sport, strings.TrimSpace(in.PlanName), start, end, fee,
	).Scan(&enrollmentID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	row, err := s.GetEnrollment(ctx, enrollmentID)
	if err != nil {
		return nil, err
	}
	s.notifyStudentRegistrationConfirmation(ctx, row)
	return row, nil
}

func (s *Service) GetEnrollment(ctx context.Context, enrollmentID int64) (*StudentRow, error) {
	var r StudentRow
	var sport sql.NullInt64
	var fee sql.NullFloat64
	err := s.DB.QueryRowContext(ctx, `
SELECT e.id, s.id, e.facility_id, s.first_name, s.last_name,
       COALESCE(s.contact_email,''), COALESCE(s.contact_phone,''), COALESCE(s.whatsapp,''),
       COALESCE(s.photo_url,''),
       e.sport_id, COALESCE(e.plan_name,''),
       COALESCE(e.start_date::text,''), COALESCE(e.end_date::text,''), e.actual_fee,
       e.status, e.created_at::text
FROM student_enrollments e
JOIN students s ON s.id = e.student_id
WHERE e.id=$1`, enrollmentID).Scan(
		&r.EnrollmentID, &r.StudentID, &r.FacilityID, &r.FirstName, &r.LastName,
		&r.Email, &r.Phone, &r.WhatsApp, &r.PhotoURL, &sport, &r.PlanName,
		&r.StartDate, &r.EndDate, &fee, &r.Status, &r.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	if sport.Valid {
		v := sport.Int64
		r.SportID = &v
	}
	if fee.Valid {
		v := fee.Float64
		r.ActualFee = &v
	}
	return &r, nil
}

func (s *Service) ListAttendance(ctx context.Context, facilityID int64, date string) ([]AttendanceRow, error) {
	if facilityID <= 0 {
		return nil, fmt.Errorf("invalid_facility")
	}
	if err := validateDate(date); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT e.id, s.id, e.facility_id, s.first_name, s.last_name, COALESCE(s.photo_url,''),
       COALESCE(s.contact_phone,''), COALESCE(e.plan_name,''),
       $2::text,
       COALESCE(a.status,''), a.id, COALESCE(a.notes,'')
FROM student_enrollments e
JOIN students s ON s.id = e.student_id
LEFT JOIN student_attendance a
  ON a.enrollment_id = e.id AND a.attendance_date = $2::date
WHERE e.facility_id=$1 AND e.status='active'
ORDER BY s.first_name, s.last_name, e.id`, facilityID, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AttendanceRow{}
	for rows.Next() {
		var r AttendanceRow
		var attID sql.NullInt64
		if err := rows.Scan(
			&r.EnrollmentID, &r.StudentID, &r.FacilityID, &r.FirstName, &r.LastName, &r.PhotoURL,
			&r.Phone, &r.PlanName, &r.AttendanceDate, &r.Status, &attID, &r.Notes,
		); err != nil {
			return nil, err
		}
		if attID.Valid {
			v := attID.Int64
			r.AttendanceID = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type MarkAttendanceResult struct {
	Attendance AttendanceRow         `json:"attendance"`
	Push       AttendancePushResult  `json:"push"`
	Activated  bool                  `json:"activated,omitempty"`
}

func (s *Service) MarkAttendance(ctx context.Context, facilityID int64, in MarkAttendanceInput) (*MarkAttendanceResult, error) {
	status := strings.ToLower(strings.TrimSpace(in.Status))
	if facilityID <= 0 || in.EnrollmentID <= 0 {
		return nil, fmt.Errorf("invalid_attendance")
	}
	if status != "present" && status != "absent" && status != "leave" {
		return nil, fmt.Errorf("invalid_status")
	}
	if err := validateDate(in.Date); err != nil {
		return nil, err
	}
	actorID := int64(0)
	if in.MarkedBy != nil {
		actorID = *in.MarkedBy
	}
	if actorID > 0 {
		if err := s.EnsureCanEditStudentAttendance(ctx, facilityID, actorID, in.Role, in.Date); err != nil {
			return nil, err
		}
	}

	var enrFacility int64
	var enrStatus string
	err := s.DB.QueryRowContext(ctx, `
SELECT facility_id, status FROM student_enrollments WHERE id=$1`, in.EnrollmentID).
		Scan(&enrFacility, &enrStatus)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("enrollment_not_found")
	}
	if err != nil {
		return nil, err
	}
	if enrFacility != facilityID {
		return nil, fmt.Errorf("facility_mismatch")
	}

	var markedBy interface{}
	if in.MarkedBy != nil {
		markedBy = *in.MarkedBy
	}

	_, err = s.DB.ExecContext(ctx, `
INSERT INTO student_attendance (facility_id, enrollment_id, attendance_date, status, marked_by, notes)
VALUES ($1,$2,$3::date,$4,$5,NULLIF($6,''))
ON CONFLICT (enrollment_id, attendance_date) DO UPDATE
SET status=EXCLUDED.status,
    marked_by=EXCLUDED.marked_by,
    notes=EXCLUDED.notes,
    updated_at=NOW()`,
		facilityID, in.EnrollmentID, in.Date, status, markedBy, strings.TrimSpace(in.Notes),
	)
	if err != nil {
		return nil, err
	}

	activated := false
	// Present / late-as-leave on inactive → reactivate (legacy mark_attendance_status1).
	if status == "present" && enrStatus != "active" {
		_, _ = s.DB.ExecContext(ctx, `
UPDATE student_enrollments SET status='active', updated_at=NOW() WHERE id=$1`, in.EnrollmentID)
		activated = true
	}

	var att AttendanceRow
	list, err := s.ListAttendance(ctx, facilityID, in.Date)
	if err != nil {
		return nil, err
	}
	found := false
	for i := range list {
		if list[i].EnrollmentID == in.EnrollmentID {
			att = list[i]
			found = true
			break
		}
	}
	if !found {
		// Inactive enrollments are omitted from ListAttendance — synthesize row.
		row, gerr := s.GetEnrollment(ctx, in.EnrollmentID)
		if gerr != nil {
			return nil, fmt.Errorf("attendance_not_found")
		}
		att = AttendanceRow{
			EnrollmentID:   row.EnrollmentID,
			StudentID:      row.StudentID,
			FacilityID:     row.FacilityID,
			FirstName:      row.FirstName,
			LastName:       row.LastName,
			Phone:          row.Phone,
			PlanName:       row.PlanName,
			AttendanceDate: in.Date,
			Status:         status,
			Notes:          strings.TrimSpace(in.Notes),
		}
	}

	push := s.notifyStudentAttendance(ctx, facilityID, in.EnrollmentID, in.Date, status)
	return &MarkAttendanceResult{Attendance: att, Push: push, Activated: activated}, nil
}

func (s *Service) UpdateEnrollmentStatus(ctx context.Context, facilityID, enrollmentID int64, status string) (*StudentRow, error) {
	st := strings.ToLower(strings.TrimSpace(status))
	if facilityID <= 0 || enrollmentID <= 0 {
		return nil, fmt.Errorf("invalid_enrollment")
	}
	if st != "active" && st != "inactive" && st != "completed" {
		return nil, fmt.Errorf("invalid_status")
	}
	res, err := s.DB.ExecContext(ctx, `
UPDATE student_enrollments SET status=$1, updated_at=NOW()
WHERE id=$2 AND facility_id=$3`, st, enrollmentID, facilityID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("enrollment_not_found")
	}
	return s.GetEnrollment(ctx, enrollmentID)
}

func (s *Service) UpdateStudent(ctx context.Context, facilityID, studentID int64, in UpdateStudentInput) (*StudentRow, error) {
	if facilityID <= 0 || studentID <= 0 {
		return nil, fmt.Errorf("invalid_student")
	}
	first := strings.TrimSpace(in.FirstName)
	last := strings.TrimSpace(in.LastName)
	if first == "" {
		return nil, fmt.Errorf("invalid_student")
	}
	// Ensure student belongs to facility via an enrollment.
	var enrollmentID int64
	err := s.DB.QueryRowContext(ctx, `
SELECT id FROM student_enrollments WHERE facility_id=$1 AND student_id=$2
ORDER BY id DESC LIMIT 1`, facilityID, studentID).Scan(&enrollmentID)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("student_not_found")
	}
	if err != nil {
		return nil, err
	}
	_, err = s.DB.ExecContext(ctx, `
UPDATE students SET
  first_name=$1, last_name=$2,
  contact_email=NULLIF($3,''), contact_phone=NULLIF($4,''), whatsapp=NULLIF($5,''),
  updated_at=NOW()
WHERE id=$6`,
		first, last,
		strings.TrimSpace(in.Email),
		strings.TrimSpace(in.Phone),
		strings.TrimSpace(in.WhatsApp),
		studentID,
	)
	if err != nil {
		return nil, err
	}
	return s.GetEnrollment(ctx, enrollmentID)
}

func (s *Service) UpdatePlan(ctx context.Context, facilityID, enrollmentID int64, in UpdatePlanInput) (*StudentRow, error) {
	if facilityID <= 0 || enrollmentID <= 0 {
		return nil, fmt.Errorf("invalid_enrollment")
	}
	plan := strings.TrimSpace(in.PlanName)
	if in.StartDate != "" {
		if err := validateDate(in.StartDate); err != nil {
			return nil, err
		}
	}
	if in.EndDate != "" {
		if err := validateDate(in.EndDate); err != nil {
			return nil, err
		}
	}
	var start, end, fee interface{}
	if strings.TrimSpace(in.StartDate) != "" {
		start = in.StartDate
	}
	if strings.TrimSpace(in.EndDate) != "" {
		end = in.EndDate
	}
	if in.ActualFee != nil {
		fee = *in.ActualFee
	}
	res, err := s.DB.ExecContext(ctx, `
UPDATE student_enrollments SET
  plan_name=NULLIF($1,''),
  start_date=COALESCE($2::date, start_date),
  end_date=COALESCE($3::date, end_date),
  actual_fee=COALESCE($4, actual_fee),
  updated_at=NOW()
WHERE id=$5 AND facility_id=$6`,
		plan, start, end, fee, enrollmentID, facilityID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("enrollment_not_found")
	}
	return s.GetEnrollment(ctx, enrollmentID)
}

func (s *Service) ListPlans(ctx context.Context, facilityID int64) ([]PlanRow, error) {
	if facilityID <= 0 {
		return nil, fmt.Errorf("invalid_facility")
	}

	// Prefer persistent catalogue; merge enrollment-only names.
	byName := map[string]*PlanRow{}
	order := []string{}

	add := func(p PlanRow) {
		key := strings.ToLower(strings.TrimSpace(p.PlanName))
		if key == "" {
			return
		}
		if cur, ok := byName[key]; ok {
			if p.PlanID > 0 && cur.PlanID == 0 {
				cur.PlanID = p.PlanID
			}
			if p.Count > cur.Count {
				cur.Count = p.Count
			}
			if p.TotalFee > 0 && cur.TotalFee == 0 {
				cur.TotalFee = p.TotalFee
			}
			if p.DaysPerWeek > 0 && cur.DaysPerWeek == 0 {
				cur.DaysPerWeek = p.DaysPerWeek
			}
			if p.Description != "" && cur.Description == "" {
				cur.Description = p.Description
			}
			if p.StartTime != "" && cur.StartTime == "" {
				cur.StartTime = p.StartTime
			}
			if p.EndTime != "" && cur.EndTime == "" {
				cur.EndTime = p.EndTime
			}
			if p.CoachingDays != "" && cur.CoachingDays == "" {
				cur.CoachingDays = p.CoachingDays
			}
			return
		}
		cp := p
		byName[key] = &cp
		order = append(order, key)
	}

	catRows, err := s.DB.QueryContext(ctx, `
SELECT id, name, COALESCE(total_fee,0), COALESCE(days_per_week,0), COALESCE(description,''),
  COALESCE(start_time,''), COALESCE(end_time,''), COALESCE(coaching_days,''),
  (SELECT COUNT(*)::int FROM student_enrollments e
   WHERE e.facility_id=$1 AND LOWER(TRIM(COALESCE(e.plan_name,''))) = LOWER(TRIM(p.name)))
FROM student_coaching_plans p
WHERE p.facility_id=$1 AND p.status='active'
ORDER BY p.name`, facilityID)
	if err == nil {
		defer catRows.Close()
		for catRows.Next() {
			var p PlanRow
			if err := catRows.Scan(&p.PlanID, &p.PlanName, &p.TotalFee, &p.DaysPerWeek, &p.Description, &p.StartTime, &p.EndTime, &p.CoachingDays, &p.Count); err != nil {
				return nil, err
			}
			add(p)
		}
		if err := catRows.Err(); err != nil {
			return nil, err
		}
	}

	// Soft-removed batches stay in DB (status=inactive) — never resurface from enrollments.
	inactive := map[string]struct{}{}
	if invRows, invErr := s.DB.QueryContext(ctx, `
SELECT LOWER(TRIM(name)) FROM student_coaching_plans
WHERE facility_id=$1 AND status='inactive'`, facilityID); invErr == nil {
		defer invRows.Close()
		for invRows.Next() {
			var key string
			if err := invRows.Scan(&key); err == nil && key != "" {
				inactive[key] = struct{}{}
			}
		}
	}

	enrRows, err := s.DB.QueryContext(ctx, `
SELECT COALESCE(NULLIF(TRIM(plan_name),''),'(No batch)') AS plan_name, COUNT(*)::int
FROM student_enrollments
WHERE facility_id=$1
GROUP BY 1
ORDER BY 1`, facilityID)
	if err != nil {
		return nil, err
	}
	defer enrRows.Close()
	for enrRows.Next() {
		var p PlanRow
		if err := enrRows.Scan(&p.PlanName, &p.Count); err != nil {
			return nil, err
		}
		key := strings.ToLower(strings.TrimSpace(p.PlanName))
		if _, gone := inactive[key]; gone {
			continue
		}
		// Placeholder for unassigned enrollments — not a real batch to manage.
		if key == "(no batch)" || key == "" {
			continue
		}
		add(p)
	}
	if err := enrRows.Err(); err != nil {
		return nil, err
	}

	out := make([]PlanRow, 0, len(order))
	synthID := int64(1)
	for _, key := range order {
		p := *byName[key]
		if p.PlanID <= 0 {
			p.PlanID = synthID
			synthID++
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *Service) CreatePlan(ctx context.Context, facilityID int64, in CreatePlanInput) (*PlanRow, error) {
	name := strings.TrimSpace(in.PlanName)
	if facilityID <= 0 || name == "" {
		return nil, fmt.Errorf("invalid_plan")
	}
	days := in.DaysPerWeek
	if days < 0 {
		days = 0
	}
	if days > 7 {
		days = 7
	}
	desc := strings.TrimSpace(in.Description)

	coachingDays, daysFromCal, errDays := normalizeCoachingDays(in.CoachingDays)
	if errDays != nil {
		return nil, errDays
	}
	if daysFromCal > 0 {
		days = daysFromCal
	}
	startTime := strings.TrimSpace(in.StartTime)
	endTime := strings.TrimSpace(in.EndTime)
	if startTime != "" && !batchTimeRe.MatchString(startTime) {
		return nil, fmt.Errorf("invalid_start_time")
	}
	if endTime != "" && !batchTimeRe.MatchString(endTime) {
		return nil, fmt.Errorf("invalid_end_time")
	}

	var id int64
	var fee float64
	var daysOut int
	var descOut, startOut, endOut, daysCalOut string
	err := s.DB.QueryRowContext(ctx, `
INSERT INTO student_coaching_plans (facility_id, name, total_fee, days_per_week, description, start_time, end_time, coaching_days, status)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'active')
ON CONFLICT (facility_id, name) DO UPDATE SET
  total_fee = EXCLUDED.total_fee,
  days_per_week = EXCLUDED.days_per_week,
  description = CASE WHEN EXCLUDED.description <> '' THEN EXCLUDED.description ELSE student_coaching_plans.description END,
  start_time = CASE WHEN EXCLUDED.start_time <> '' THEN EXCLUDED.start_time ELSE student_coaching_plans.start_time END,
  end_time = CASE WHEN EXCLUDED.end_time <> '' THEN EXCLUDED.end_time ELSE student_coaching_plans.end_time END,
  coaching_days = CASE WHEN EXCLUDED.coaching_days <> '' THEN EXCLUDED.coaching_days ELSE student_coaching_plans.coaching_days END,
  status = 'active',
  updated_at = NOW()
RETURNING id, total_fee, days_per_week, COALESCE(description,''), COALESCE(start_time,''), COALESCE(end_time,''), COALESCE(coaching_days,'')`,
		facilityID, name, in.TotalFee, days, desc, startTime, endTime, coachingDays,
	).Scan(&id, &fee, &daysOut, &descOut, &startOut, &endOut, &daysCalOut)
	if err != nil {
		return nil, err
	}

	var count int
	_ = s.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM student_enrollments
WHERE facility_id=$1 AND LOWER(TRIM(COALESCE(plan_name,''))) = LOWER($2)`,
		facilityID, name).Scan(&count)

	row := &PlanRow{
		PlanID: id, PlanName: name, Count: count,
		TotalFee: fee, DaysPerWeek: daysOut, Description: descOut,
		StartTime: startOut, EndTime: endOut, CoachingDays: daysCalOut,
	}
	s.syncCoachingActiveBlock(ctx, facilityID, name, row)
	return row, nil
}

// normalizeCoachingDays keeps weekday numbers 1=Mon … 7=Sun, comma-separated.
func normalizeCoachingDays(raw string) (string, int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0, nil
	}
	seen := map[int]struct{}{}
	var days []int
	for _, part := range strings.Split(raw, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 1 || n > 7 {
			return "", 0, fmt.Errorf("invalid_coaching_days")
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		days = append(days, n)
	}
	sort.Ints(days)
	out := make([]string, len(days))
	for i, n := range days {
		out[i] = strconv.Itoa(n)
	}
	return strings.Join(out, ","), len(days), nil
}

// UpdateCoachingPlan updates an active batch. Renames also sync enrollment plan_name.
// Does not delete data.
func (s *Service) UpdateCoachingPlan(ctx context.Context, facilityID, planID int64, in UpdateCoachingPlanInput) (*PlanRow, error) {
	if facilityID <= 0 || planID <= 0 {
		return nil, fmt.Errorf("invalid_plan")
	}
	var oldName string
	err := s.DB.QueryRowContext(ctx, `
SELECT name FROM student_coaching_plans
WHERE id=$1 AND facility_id=$2 AND status='active'`, planID, facilityID).Scan(&oldName)
	if err != nil {
		return nil, fmt.Errorf("plan_not_found")
	}

	newName := strings.TrimSpace(in.PlanName)
	if newName == "" {
		newName = oldName
	}
	if in.StartTime != nil {
		t := strings.TrimSpace(*in.StartTime)
		if t != "" && !batchTimeRe.MatchString(t) {
			return nil, fmt.Errorf("invalid_start_time")
		}
	}
	if in.EndTime != nil {
		t := strings.TrimSpace(*in.EndTime)
		if t != "" && !batchTimeRe.MatchString(t) {
			return nil, fmt.Errorf("invalid_end_time")
		}
	}
	if in.DaysPerWeek != nil {
		d := *in.DaysPerWeek
		if d < 0 {
			d = 0
		}
		if d > 7 {
			d = 7
		}
		in.DaysPerWeek = &d
	}
	var coachingDays *string
	if in.CoachingDays != nil {
		norm, n, errDays := normalizeCoachingDays(*in.CoachingDays)
		if errDays != nil {
			return nil, errDays
		}
		coachingDays = &norm
		if n > 0 {
			in.DaysPerWeek = &n
		}
	}

	_, err = s.DB.ExecContext(ctx, `
UPDATE student_coaching_plans SET
  name = $3,
  total_fee = COALESCE($4, total_fee),
  days_per_week = COALESCE($5, days_per_week),
  description = COALESCE($6, description),
  start_time = COALESCE($7, start_time),
  end_time = COALESCE($8, end_time),
  coaching_days = COALESCE($9, coaching_days),
  updated_at = NOW()
WHERE id=$1 AND facility_id=$2 AND status='active'`,
		planID, facilityID, newName,
		in.TotalFee, in.DaysPerWeek, in.Description, in.StartTime, in.EndTime, coachingDays,
	)
	if err != nil {
		return nil, err
	}

	if !strings.EqualFold(strings.TrimSpace(oldName), newName) {
		_, _ = s.DB.ExecContext(ctx, `
UPDATE student_enrollments SET plan_name=$3, updated_at=NOW()
WHERE facility_id=$1 AND LOWER(TRIM(COALESCE(plan_name,''))) = LOWER(TRIM($2))`,
			facilityID, oldName, newName)
	}

	list, err := s.ListPlans(ctx, facilityID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].PlanID == planID || strings.EqualFold(list[i].PlanName, newName) {
			s.syncCoachingActiveBlock(ctx, facilityID, oldName, &list[i])
			return &list[i], nil
		}
	}
	row := &PlanRow{PlanID: planID, PlanName: newName}
	s.syncCoachingActiveBlock(ctx, facilityID, oldName, row)
	return row, nil
}

// SoftDeleteCoachingPlan hides a batch (status=inactive). Student rows stay.
// Enrollments for that batch name become inactive. planName covers enrollment-only batches.
func (s *Service) SoftDeleteCoachingPlan(ctx context.Context, facilityID, planID int64, planName string) error {
	if facilityID <= 0 {
		return fmt.Errorf("invalid_plan")
	}
	if planID > 0 {
		var removed string
		q := `
UPDATE student_coaching_plans
SET status='inactive', updated_at=NOW()
WHERE id=$1 AND facility_id=$2 AND status='active'`
		args := []interface{}{planID, facilityID}
		if strings.TrimSpace(planName) != "" {
			q += ` AND LOWER(TRIM(name))=LOWER(TRIM($3))`
			args = append(args, strings.TrimSpace(planName))
		}
		q += ` RETURNING name`
		err := s.DB.QueryRowContext(ctx, q, args...).Scan(&removed)
		if err == nil {
			s.deactivateBatchEnrollments(ctx, facilityID, removed)
			s.removeCoachingActiveBlock(ctx, facilityID, removed)
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
	}
	name := strings.TrimSpace(planName)
	if name == "" || strings.EqualFold(name, "(No batch)") {
		return fmt.Errorf("plan_not_found")
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO student_coaching_plans (facility_id, name, status)
VALUES ($1,$2,'inactive')
ON CONFLICT (facility_id, name) DO UPDATE SET
  status='inactive',
  updated_at=NOW()`, facilityID, name)
	if err != nil {
		return err
	}
	s.deactivateBatchEnrollments(ctx, facilityID, name)
	s.removeCoachingActiveBlock(ctx, facilityID, name)
	return nil
}

func (s *Service) deactivateBatchEnrollments(ctx context.Context, facilityID int64, planName string) {
	name := strings.TrimSpace(planName)
	if facilityID <= 0 || name == "" {
		return
	}
	if _, err := s.DB.ExecContext(ctx, `
UPDATE student_enrollments
SET status='inactive', updated_at=NOW()
WHERE facility_id=$1
  AND status='active'
  AND LOWER(TRIM(COALESCE(plan_name,''))) = LOWER(TRIM($2))`, facilityID, name); err != nil {
		log.Printf("batch enrollment deactivate facility=%d plan=%q: %v", facilityID, name, err)
	}
}

func validateDate(date string) error {
	date = strings.TrimSpace(date)
	if date == "" {
		return fmt.Errorf("date_required")
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return fmt.Errorf("invalid_date")
	}
	return nil
}
