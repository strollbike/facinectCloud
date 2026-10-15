package app

import (
	"context"
	"fmt"
	"time"
)

func (s *Service) liveStudentMyEnrollments(ctx context.Context, last10 string) ([]map[string]any, error) {
	match := fmt.Sprintf("(%s = $1 OR %s = $1)",
		phoneLast10SQL("s.whatsapp"),
		phoneLast10SQL("s.contact_phone"),
	)
	q := fmt.Sprintf(`
SELECT e.id, s.id,
       TRIM(COALESCE(s.first_name,'') || ' ' || COALESCE(s.last_name,'')) AS student_name,
       '' AS level,
       e.status,
       COALESCE(e.plan_name,'') AS plan_name,
       0 AS plan_id,
       e.facility_id,
       COALESCE(NULLIF(af.name,''), NULLIF(f.name,''), '') AS facility_name,
       COALESCE(af.logo_url,'') AS facility_logo,
       COALESCE(e.sport_id, 0) AS sport_id,
       '' AS sport_name
FROM student_enrollments e
JOIN students s ON s.id = e.student_id
LEFT JOIN app_facilities af ON af.id = e.facility_id
LEFT JOIN facilities f ON f.id = e.facility_id
WHERE %s
ORDER BY CASE WHEN LOWER(TRIM(COALESCE(e.status,''))) = 'active' THEN 0 ELSE 1 END, e.id DESC`, match)

	rows, err := s.DB.QueryContext(ctx, q, last10)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	month := time.Now().Format("2006-01")
	list := []map[string]any{}
	for rows.Next() {
		var enrollID, studentID, planID, fid, sportID int64
		var sname, level, status, planName, fname, logo, sportName string
		if err := rows.Scan(&enrollID, &studentID, &sname, &level, &status, &planName, &planID, &fid,
			&fname, &logo, &sportID, &sportName); err != nil {
			return nil, err
		}
		if sname == "" {
			sname = "Student"
		}
		present, absent := 0, 0
		_ = s.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FILTER (WHERE status='present'), COUNT(*) FILTER (WHERE status='absent')
FROM student_attendance
WHERE enrollment_id=$1 AND to_char(attendance_date,'YYYY-MM')=$2`, enrollID, month).
			Scan(&present, &absent)
		list = append(list, map[string]any{
			"enrollmentId": enrollID, "studentId": studentID, "studentName": sname,
			"level": level, "status": status, "planId": planID, "planName": planName,
			"facilityId": fid, "facilityName": fname, "facilityLogoUrl": logo,
			"sportId": sportID, "sportName": sportName,
			"currentMonth": month, "presentCount": present, "absentCount": absent,
		})
	}
	return list, rows.Err()
}

func (s *Service) appStudentEnrollmentsByUserOrWA(ctx context.Context, uid int64, last10 string) ([]map[string]any, error) {
	waMatch := phoneLast10SQL("u.whatsapp_no")
	q := fmt.Sprintf(`
SELECT e.id, e.student_id, e.student_name, e.level, e.status, e.plan_id, e.plan_name,
       e.facility_id, COALESCE(f.name,''), COALESCE(f.logo_url,''), e.sport_id, e.sport_name
FROM app_student_enrollments e
LEFT JOIN app_facilities f ON f.id=e.facility_id
LEFT JOIN app_users u ON u.id = e.user_id
WHERE e.user_id=$1 OR (%s = $2 AND $2 <> '')
ORDER BY e.id DESC`, waMatch)

	rows, err := s.DB.QueryContext(ctx, q, uid, last10)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	month := time.Now().Format("2006-01")
	list := []map[string]any{}
	for rows.Next() {
		var id, sid, planID, fid, sportID int64
		var sname, level, status, planName, fname, logo, sportName string
		if err := rows.Scan(&id, &sid, &sname, &level, &status, &planID, &planName, &fid, &fname, &logo, &sportID, &sportName); err != nil {
			return nil, err
		}
		var present, absent int
		_ = s.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FILTER (WHERE status='present'), COUNT(*) FILTER (WHERE status='absent')
FROM app_student_attendance WHERE enrollment_id=$1 AND to_char(attend_date,'YYYY-MM')=$2`, id, month).
			Scan(&present, &absent)
		list = append(list, map[string]any{
			"enrollmentId": id, "studentId": sid, "studentName": sname, "level": level, "status": status,
			"planId": planID, "planName": planName, "facilityId": fid, "facilityName": fname,
			"facilityLogoUrl": logo, "sportId": sportID, "sportName": sportName,
			"currentMonth": month, "presentCount": present, "absentCount": absent,
		})
	}
	return list, rows.Err()
}

func (s *Service) studentPortalMyEnrollments(ctx context.Context, body map[string]any) (bool, string, any) {
	last10, errMsg := s.resolvePortalLast10(ctx, body)
	if errMsg != "" {
		return false, errMsg, nil
	}
	uid, _ := parseInt64Field(body, "userId")
	wa := s.resolvePortalWhatsApp(ctx, body)

	var list []map[string]any
	var err error

	if s.tableExists(ctx, "student_enrollments") {
		list, err = s.liveStudentMyEnrollments(ctx, last10)
		if err != nil {
			return false, err.Error(), nil
		}
	}
	if len(list) == 0 {
		list, err = s.appStudentEnrollmentsByUserOrWA(ctx, uid, last10)
		if err != nil {
			return false, err.Error(), nil
		}
	}

	msg := "OK"
	if len(list) == 0 {
		msg = "No student enrollments found."
	}
	return true, msg, map[string]any{
		"whatsappNo":  wa,
		"enrollments": list,
		"count":       len(list),
	}
}
