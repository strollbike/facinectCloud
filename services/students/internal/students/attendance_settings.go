package students

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

var batchTimeRe = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)

type AttendanceSettings struct {
	FacilityID               int64    `json:"facility_id"`
	BatchStartTime           string   `json:"batch_start_time"`
	AttendanceRadiusMeters   int      `json:"attendance_radius_meters"`
	FacilityLatitude         *float64 `json:"facility_latitude,omitempty"`
	FacilityLongitude        *float64 `json:"facility_longitude,omitempty"`
	ReminderOffsetMinutes    int      `json:"reminder_offset_minutes"`
}

type BatchSchedule struct {
	PlanID       int64  `json:"plan_id"`
	PlanName     string `json:"plan_name"`
	StartTime    string `json:"start_time"`
	EndTime      string `json:"end_time"`
	CoachingDays string `json:"coaching_days,omitempty"`
}

type AttendanceAccess struct {
	Date              string          `json:"date"`
	IsToday           bool            `json:"is_today"`
	IsPast            bool            `json:"is_past"`
	IsFuture          bool            `json:"is_future"`
	RoleBypass        bool            `json:"role_bypass"` // admin / sub_admin
	CanMarkCoach      bool            `json:"can_mark_coach"`
	CoachGateRequired bool            `json:"coach_gate_required"`
	CoachPresent      bool            `json:"coach_present"`
	CanEditStudents   bool            `json:"can_edit_students"`
	LocationRequired  bool            `json:"location_required"`
	RadiusMeters      int             `json:"radius_meters"`
	BatchStartTime        string          `json:"batch_start_time"` // next/earliest batch start today
	ReminderOffsetMinutes int             `json:"reminder_offset_minutes"`
	Batches               []BatchSchedule `json:"batches,omitempty"`
	FacilityLatitude  *float64        `json:"facility_latitude,omitempty"`
	FacilityLongitude *float64        `json:"facility_longitude,omitempty"`
	Message           string          `json:"message,omitempty"`
}

func (s *Service) ListBatchSchedules(ctx context.Context, facilityID int64) ([]BatchSchedule, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, name, COALESCE(start_time,''), COALESCE(end_time,''), COALESCE(coaching_days,'')
FROM student_coaching_plans
WHERE facility_id=$1 AND status='active' AND COALESCE(start_time,'') <> ''
ORDER BY start_time ASC, name ASC`, facilityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BatchSchedule{}
	for rows.Next() {
		var b BatchSchedule
		if err := rows.Scan(&b.PlanID, &b.PlanName, &b.StartTime, &b.EndTime, &b.CoachingDays); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func earliestBatchStart(batches []BatchSchedule, fallback string) string {
	for _, b := range batches {
		if strings.TrimSpace(b.StartTime) != "" {
			return b.StartTime
		}
	}
	return fallback
}

func isAttendanceBypassRole(role string) bool {
	r := strings.ToLower(strings.TrimSpace(role))
	return r == "admin" || r == "sub_admin"
}

func (s *Service) GetAttendanceSettings(ctx context.Context, facilityID int64) (*AttendanceSettings, error) {
	if facilityID <= 0 {
		return nil, fmt.Errorf("invalid_facility")
	}
	out := &AttendanceSettings{
		FacilityID:             facilityID,
		BatchStartTime:         "06:00",
		AttendanceRadiusMeters: 150,
		ReminderOffsetMinutes:  52,
	}
	var batch string
	var radius, reminder int
	var lat, lng sql.NullFloat64
	err := s.DB.QueryRowContext(ctx, `
SELECT batch_start_time, attendance_radius_meters, facility_latitude, facility_longitude,
       COALESCE(reminder_offset_minutes, 52)
FROM facility_attendance_settings WHERE facility_id=$1`, facilityID).
		Scan(&batch, &radius, &lat, &lng, &reminder)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == nil {
		if strings.TrimSpace(batch) != "" {
			out.BatchStartTime = batch
		}
		if radius > 0 {
			out.AttendanceRadiusMeters = radius
		}
		if reminder > 0 {
			out.ReminderOffsetMinutes = reminder
		}
		if lat.Valid && lng.Valid {
			la, lo := lat.Float64, lng.Float64
			out.FacilityLatitude = &la
			out.FacilityLongitude = &lo
		}
	}
	// Fallback coords from app_facilities when settings lat/lng empty (same Postgres).
	if out.FacilityLatitude == nil || out.FacilityLongitude == nil {
		var alat, alng sql.NullFloat64
		if err2 := s.DB.QueryRowContext(ctx, `
SELECT latitude, longitude FROM app_facilities WHERE id=$1`, facilityID).Scan(&alat, &alng); err2 == nil {
			if alat.Valid && alng.Valid {
				la, lo := alat.Float64, alng.Float64
				out.FacilityLatitude = &la
				out.FacilityLongitude = &lo
			}
		}
	}
	return out, nil
}

type UpdateAttendanceSettingsInput struct {
	BatchStartTime         *string  `json:"batch_start_time"`
	AttendanceRadiusMeters *int     `json:"attendance_radius_meters"`
	ReminderOffsetMinutes  *int     `json:"reminder_offset_minutes"`
	FacilityLatitude       *float64 `json:"facility_latitude"`
	FacilityLongitude      *float64 `json:"facility_longitude"`
	UpdateBatch            bool     `json:"-"`
	UpdateRadius           bool     `json:"-"`
	UpdateReminder         bool     `json:"-"`
	UpdateCoords           bool     `json:"-"`
}

func (s *Service) UpdateAttendanceSettings(ctx context.Context, facilityID int64, in UpdateAttendanceSettingsInput) (*AttendanceSettings, error) {
	if facilityID <= 0 {
		return nil, fmt.Errorf("invalid_facility")
	}
	cur, err := s.GetAttendanceSettings(ctx, facilityID)
	if err != nil {
		return nil, err
	}
	batch := cur.BatchStartTime
	radius := cur.AttendanceRadiusMeters
	reminder := cur.ReminderOffsetMinutes
	var lat, lng *float64 = cur.FacilityLatitude, cur.FacilityLongitude

	if in.UpdateBatch {
		if in.BatchStartTime == nil || !batchTimeRe.MatchString(strings.TrimSpace(*in.BatchStartTime)) {
			return nil, fmt.Errorf("invalid_batch_start_time")
		}
		batch = strings.TrimSpace(*in.BatchStartTime)
	}
	if in.UpdateRadius {
		if in.AttendanceRadiusMeters == nil || *in.AttendanceRadiusMeters < 20 || *in.AttendanceRadiusMeters > 5000 {
			return nil, fmt.Errorf("invalid_attendance_radius")
		}
		radius = *in.AttendanceRadiusMeters
	}
	if in.UpdateReminder {
		if in.ReminderOffsetMinutes == nil || *in.ReminderOffsetMinutes < 1 || *in.ReminderOffsetMinutes > 180 {
			return nil, fmt.Errorf("invalid_reminder_offset")
		}
		reminder = *in.ReminderOffsetMinutes
	}
	if in.UpdateCoords {
		lat, lng = in.FacilityLatitude, in.FacilityLongitude
		if (lat == nil) != (lng == nil) {
			return nil, fmt.Errorf("invalid_facility_coords")
		}
	}

	_, err = s.DB.ExecContext(ctx, `
INSERT INTO facility_attendance_settings (
  facility_id, batch_start_time, attendance_radius_meters, facility_latitude, facility_longitude,
  reminder_offset_minutes, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,NOW())
ON CONFLICT (facility_id) DO UPDATE SET
  batch_start_time=EXCLUDED.batch_start_time,
  attendance_radius_meters=EXCLUDED.attendance_radius_meters,
  facility_latitude=EXCLUDED.facility_latitude,
  facility_longitude=EXCLUDED.facility_longitude,
  reminder_offset_minutes=EXCLUDED.reminder_offset_minutes,
  updated_at=NOW()`,
		facilityID, batch, radius, lat, lng, reminder,
	)
	if err != nil {
		return nil, err
	}
	return s.GetAttendanceSettings(ctx, facilityID)
}

func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371000.0
	toRad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := toRad(lat2 - lat1)
	dLon := toRad(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func (s *Service) CoachWasPresent(ctx context.Context, facilityID, coachUserID int64, date string) (bool, error) {
	var status string
	err := s.DB.QueryRowContext(ctx, `
SELECT status FROM coach_attendance
WHERE facility_id=$1 AND coach_user_id=$2 AND attendance_date=$3::date`,
		facilityID, coachUserID, date).Scan(&status)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.EqualFold(status, "present"), nil
}

func (s *Service) ResolveAttendanceAccess(ctx context.Context, facilityID, userID int64, role, date string) (*AttendanceAccess, error) {
	if facilityID <= 0 || userID <= 0 {
		return nil, fmt.Errorf("invalid_request")
	}
	if strings.TrimSpace(date) == "" {
		date = time.Now().In(istLoc()).Format("2006-01-02")
	}
	if err := validateDate(date); err != nil {
		return nil, err
	}
	settings, err := s.GetAttendanceSettings(ctx, facilityID)
	if err != nil {
		return nil, err
	}
	batches, _ := s.ListBatchSchedules(ctx, facilityID)
	batchStart := earliestBatchStart(batches, settings.BatchStartTime)
	today := time.Now().In(istLoc()).Format("2006-01-02")
	isToday := date == today
	isPast := date < today
	isFuture := date > today
	bypass := isAttendanceBypassRole(role)
	present, err := s.CoachWasPresent(ctx, facilityID, userID, date)
	if err != nil {
		return nil, err
	}

	out := &AttendanceAccess{
		Date:              date,
		IsToday:           isToday,
		IsPast:            isPast,
		IsFuture:          isFuture,
		RoleBypass:        bypass,
		CoachPresent:      present,
		RadiusMeters:            settings.AttendanceRadiusMeters,
		BatchStartTime:          batchStart,
		ReminderOffsetMinutes:   settings.ReminderOffsetMinutes,
		Batches:           batches,
		FacilityLatitude:  settings.FacilityLatitude,
		FacilityLongitude: settings.FacilityLongitude,
	}

	if isFuture {
		out.CanMarkCoach = false
		out.CoachGateRequired = !bypass
		out.CanEditStudents = false
		out.LocationRequired = false
		out.Message = "Future dates are disabled for attendance."
		return out, nil
	}

	if bypass {
		out.CanMarkCoach = false
		out.CoachGateRequired = false
		out.CanEditStudents = true
		out.LocationRequired = false
		out.Message = "Admin access — no coach or location gate."
		return out, nil
	}

	if isPast {
		// Past: no coach mark; list visible; edit only if this coach was present that day.
		out.CanMarkCoach = false
		out.CoachGateRequired = false
		out.CanEditStudents = present
		out.LocationRequired = false
		if present {
			out.Message = "Past day — you can edit (you were present)."
		} else {
			out.Message = "Past day — view only. Only coaches present that day can edit."
		}
		return out, nil
	}

	// Today
	out.CanMarkCoach = true
	out.CoachGateRequired = true
	out.LocationRequired = true
	out.CanEditStudents = present
	if !present {
		out.Message = "Mark Present at the facility location to unlock student attendance."
	} else {
		out.Message = "Coach present — student attendance unlocked."
	}
	return out, nil
}

func (s *Service) EnsureCanEditStudentAttendance(ctx context.Context, facilityID, userID int64, role, date string) error {
	access, err := s.ResolveAttendanceAccess(ctx, facilityID, userID, role, date)
	if err != nil {
		return err
	}
	if access.CanEditStudents {
		return nil
	}
	if access.IsFuture {
		return fmt.Errorf("future_date_not_allowed")
	}
	if access.IsPast {
		return fmt.Errorf("past_edit_requires_present_coach")
	}
	return fmt.Errorf("coach_present_required")
}
