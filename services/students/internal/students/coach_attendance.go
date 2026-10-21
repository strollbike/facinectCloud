package students

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type CoachAttendanceRow struct {
	ID             int64  `json:"id"`
	FacilityID     int64  `json:"facility_id"`
	CoachUserID    int64  `json:"coach_user_id"`
	AttendanceDate string `json:"attendance_date"`
	Status         string `json:"status"`
	SportID        string `json:"sport_id,omitempty"`
	Notes          string `json:"notes,omitempty"`
	Marked         bool   `json:"marked"`
}

type MarkCoachAttendanceInput struct {
	CoachUserID int64    `json:"coach_user_id"`
	Date        string   `json:"date"`
	Status      string   `json:"status"`
	SportID     string   `json:"sport_id"`
	Notes       string   `json:"notes"`
	Latitude    *float64 `json:"latitude"`
	Longitude   *float64 `json:"longitude"`
	Role        string   `json:"-"` // filled by HTTP layer from JWT
}

func (s *Service) GetCoachAttendance(ctx context.Context, facilityID, coachUserID int64, date, sportID string) (*CoachAttendanceRow, error) {
	if facilityID <= 0 || coachUserID <= 0 {
		return nil, fmt.Errorf("invalid_request")
	}
	if strings.TrimSpace(date) == "" {
		date = time.Now().In(istLoc()).Format("2006-01-02")
	}
	if err := validateDate(date); err != nil {
		return nil, err
	}
	out := &CoachAttendanceRow{
		FacilityID:     facilityID,
		CoachUserID:    coachUserID,
		AttendanceDate: date,
		Status:         "",
		SportID:        strings.TrimSpace(sportID),
		Marked:         false,
	}
	var id int64
	var status, sid, notes string
	err := s.DB.QueryRowContext(ctx, `
SELECT id, status, COALESCE(sport_id,''), COALESCE(notes,'')
FROM coach_attendance
WHERE facility_id=$1 AND coach_user_id=$2 AND attendance_date=$3::date`,
		facilityID, coachUserID, date).Scan(&id, &status, &sid, &notes)
	if err == sql.ErrNoRows {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out.ID = id
	out.Status = status
	out.SportID = sid
	out.Notes = notes
	out.Marked = true
	return out, nil
}

func (s *Service) MarkCoachAttendance(ctx context.Context, facilityID int64, in MarkCoachAttendanceInput) (*CoachAttendanceRow, error) {
	if facilityID <= 0 || in.CoachUserID <= 0 {
		return nil, fmt.Errorf("invalid_request")
	}
	status := strings.ToLower(strings.TrimSpace(in.Status))
	if status != "present" && status != "absent" {
		return nil, fmt.Errorf("invalid_status")
	}
	date := strings.TrimSpace(in.Date)
	if date == "" {
		date = time.Now().In(istLoc()).Format("2006-01-02")
	}
	if err := validateDate(date); err != nil {
		return nil, err
	}

	today := time.Now().In(istLoc()).Format("2006-01-02")
	bypass := isAttendanceBypassRole(in.Role)

	// Past / future: coaches cannot mark; admins shouldn't need the gate.
	if date != today {
		if date > today {
			return nil, fmt.Errorf("future_date_not_allowed")
		}
		return nil, fmt.Errorf("past_coach_attendance_disabled")
	}
	if bypass {
		return nil, fmt.Errorf("admin_bypass_no_coach_mark")
	}

	// Location required when marking present today.
	if status == "present" {
		settings, err := s.GetAttendanceSettings(ctx, facilityID)
		if err != nil {
			return nil, err
		}
		if settings.FacilityLatitude == nil || settings.FacilityLongitude == nil {
			return nil, fmt.Errorf("facility_location_not_configured")
		}
		if in.Latitude == nil || in.Longitude == nil {
			return nil, fmt.Errorf("location_required")
		}
		dist := haversineMeters(*settings.FacilityLatitude, *settings.FacilityLongitude, *in.Latitude, *in.Longitude)
		if dist > float64(settings.AttendanceRadiusMeters) {
			return nil, fmt.Errorf("outside_facility_radius")
		}
	}

	_, err := s.DB.ExecContext(ctx, `
INSERT INTO coach_attendance (facility_id, coach_user_id, attendance_date, status, sport_id, notes)
VALUES ($1,$2,$3::date,$4,COALESCE($5,''),COALESCE($6,''))
ON CONFLICT (facility_id, coach_user_id, attendance_date) DO UPDATE SET
  status=EXCLUDED.status,
  sport_id=CASE WHEN EXCLUDED.sport_id <> '' THEN EXCLUDED.sport_id ELSE coach_attendance.sport_id END,
  notes=COALESCE(EXCLUDED.notes,''),
  updated_at=NOW()`,
		facilityID, in.CoachUserID, date, status, strings.TrimSpace(in.SportID), strings.TrimSpace(in.Notes),
	)
	if err != nil {
		return nil, err
	}
	return s.GetCoachAttendance(ctx, facilityID, in.CoachUserID, date, in.SportID)
}

func istLoc() *time.Location {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		return time.Local
	}
	return loc
}
