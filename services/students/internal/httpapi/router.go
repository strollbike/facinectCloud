package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/facinect/students/internal/authn"
	"github.com/facinect/students/internal/config"
	"github.com/facinect/students/internal/students"
)

type Server struct {
	Cfg      config.Config
	Students *students.Service
	JWT      *authn.Verifier
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/students/health", s.handleHealth)
	mux.HandleFunc("GET /v1/students/openapi.json", s.handleOpenAPI)

	mux.HandleFunc("GET /v1/students/facilities/{facilityId}/students/summary", s.withAuth(s.handleSummary))
	mux.HandleFunc("GET /v1/students/facilities/{facilityId}/students", s.withAuth(s.handleListStudents))
	mux.HandleFunc("POST /v1/students/facilities/{facilityId}/students", s.withAuth(s.handleEnroll))
	mux.HandleFunc("POST /v1/students/facilities/{facilityId}/students/{studentId}", s.withAuth(s.handleUpdateStudent))
	mux.HandleFunc("POST /v1/students/facilities/{facilityId}/students/{studentId}/photo", s.withAuth(s.handleSaveStudentPhoto))
	mux.HandleFunc("GET /v1/students/photos/{name}", s.withAuth(s.handleStudentPhoto))

	mux.HandleFunc("POST /v1/students/facilities/{facilityId}/enrollments/{enrollmentId}/status", s.withAuth(s.handleUpdateStatus))
	mux.HandleFunc("POST /v1/students/facilities/{facilityId}/enrollments/{enrollmentId}/plan", s.withAuth(s.handleUpdatePlan))

	mux.HandleFunc("GET /v1/students/facilities/{facilityId}/plans", s.withAuth(s.handleListPlans))
	mux.HandleFunc("POST /v1/students/facilities/{facilityId}/plans", s.withAuth(s.handleCreatePlan))
	mux.HandleFunc("PUT /v1/students/facilities/{facilityId}/plans/{planId}", s.withAuth(s.handleUpdateCoachingPlan))
	mux.HandleFunc("POST /v1/students/facilities/{facilityId}/plans/{planId}/remove", s.withAuth(s.handleSoftDeleteCoachingPlan))
	mux.HandleFunc("GET /v1/students/facilities/{facilityId}/coaching-leaves", s.withAuth(s.handleListCoachingLeaves))
	mux.HandleFunc("POST /v1/students/facilities/{facilityId}/coaching-leaves", s.withAuth(s.handleSetCoachingLeave))
	mux.HandleFunc("POST /v1/students/facilities/{facilityId}/coaching-leaves/cancel", s.withAuth(s.handleCancelCoachingLeave))

	mux.HandleFunc("GET /v1/students/facilities/{facilityId}/attendance", s.withAuth(s.handleListAttendance))
	mux.HandleFunc("POST /v1/students/facilities/{facilityId}/attendance", s.withAuth(s.handleMarkAttendance))
	mux.HandleFunc("GET /v1/students/facilities/{facilityId}/coach-attendance", s.withAuth(s.handleGetCoachAttendance))
	mux.HandleFunc("POST /v1/students/facilities/{facilityId}/coach-attendance", s.withAuth(s.handleMarkCoachAttendance))
	mux.HandleFunc("POST /v1/students/facilities/{facilityId}/payment-status", s.withAuth(s.handleMarkPaymentStatus))
	mux.HandleFunc("GET /v1/students/facilities/{facilityId}/enrollments/{enrollmentId}/fee-bills", s.withAuth(s.handleListFeeBills))
	mux.HandleFunc("GET /v1/students/facilities/{facilityId}/attendance-settings", s.withAuth(s.handleGetAttendanceSettings))
	mux.HandleFunc("PUT /v1/students/facilities/{facilityId}/attendance-settings", s.withAuth(s.handlePutAttendanceSettings))
	mux.HandleFunc("GET /v1/students/facilities/{facilityId}/attendance-access", s.withAuth(s.handleAttendanceAccess))

	return s.withCORS(mux)
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := s.corsOrigin(); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Service-Key")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) corsOrigin() string {
	o := strings.TrimSpace(s.Cfg.CORSOrigins)
	if o == "" {
		if s.Cfg.IsLocal() {
			return "*"
		}
		return ""
	}
	if o == "*" && !s.Cfg.IsLocal() {
		return ""
	}
	return o
}

type authCtx struct {
	Principal *authn.Principal
	ViaKey    bool
}

func (s *Server) withAuth(next func(http.ResponseWriter, *http.Request, *authCtx)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ac, err := s.authorize(r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}
		next(w, r, ac)
	}
}

func (s *Server) authorize(r *http.Request) (*authCtx, error) {
	key := strings.TrimSpace(r.Header.Get("X-Service-Key"))
	if s.Cfg.ServiceKey != "" && key != "" && key == s.Cfg.ServiceKey {
		return &authCtx{ViaKey: true}, nil
	}
	authz := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(authz), "bearer ") && s.JWT != nil {
		p, err := s.JWT.ParseAccess(strings.TrimSpace(authz[7:]))
		if err != nil {
			return nil, err
		}
		return &authCtx{Principal: p}, nil
	}
	if s.Cfg.ServiceKey == "" && s.Cfg.IsLocal() {
		return &authCtx{ViaKey: true}, nil
	}
	return nil, errUnauthorized
}

var errUnauthorized = &authError{"unauthorized"}

type authError struct{ s string }

func (e *authError) Error() string { return e.s }

func (s *Server) requireFacility(ac *authCtx, facilityID int64) error {
	if ac.ViaKey || ac.Principal == nil {
		return nil
	}
	if !ac.Principal.CanAccessFacility(facilityID) {
		return &authError{"forbidden_facility"}
	}
	return nil
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ok := true
	if err := s.Students.DB.Ping(); err != nil {
		ok = false
	}
	status := http.StatusOK
	st := "ok"
	if !ok {
		status = http.StatusServiceUnavailable
		st = "degraded"
	}
	writeJSON(w, status, map[string]interface{}{
		"service":         "students",
		"status":          st,
		"postgres":        ok,
		"service_key_set": s.Cfg.ServiceKey != "",
		"jwt_ready":       s.JWT != nil,
		"runtime":         "go",
	})
}

func (s *Server) handleListStudents(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var sportID *int64
	if v := strings.TrimSpace(r.URL.Query().Get("sport_id")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_sport_id"})
			return
		}
		sportID = &n
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	list, err := s.Students.ListStudents(r.Context(), fid, sportID, status)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"students": list})
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	n, err := s.Students.CountActive(r.Context(), fid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"active_count": n})
}

func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in students.EnrollInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Students.Enroll(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleGetCoachAttendance(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	coachID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("coach_user_id")), 10, 64)
	if coachID <= 0 && ac.Principal != nil {
		coachID = ac.Principal.UserID
	}
	if coachID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "coach_user_id_required"})
		return
	}
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	sportID := strings.TrimSpace(r.URL.Query().Get("sport_id"))
	row, err := s.Students.GetCoachAttendance(r.Context(), fid, coachID, date, sportID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"coach":   row,
		"unlocked": row != nil && row.Marked && strings.EqualFold(row.Status, "present"),
	})
}

func (s *Server) handleMarkCoachAttendance(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in students.MarkCoachAttendanceInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if in.CoachUserID <= 0 && ac.Principal != nil {
		in.CoachUserID = ac.Principal.UserID
	}
	if in.CoachUserID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "coach_user_id_required"})
		return
	}
	if ac.Principal != nil {
		in.Role = ac.Principal.Role
	}
	row, err := s.Students.MarkCoachAttendance(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"coach":    row,
		"unlocked": row != nil && row.Marked && strings.EqualFold(row.Status, "present"),
	})
}

func (s *Server) handleMarkPaymentStatus(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in students.MarkPaymentInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	result, err := s.Students.MarkStudentPayment(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":         true,
		"enrollment_id":   result.EnrollmentID,
		"payment_status":  result.PaymentStatus,
		"unpaid_count":    result.UnpaidCount,
		"billing_month":   result.BillingMonth,
		"bill_id":         result.BillID,
		"bill_amount":     result.BillAmount,
		"paid_amount":     result.PaidAmount,
		"payment_note":    result.PaymentNote,
		"payment_method":  result.PaymentMethod,
	})
}

func (s *Server) handleListFeeBills(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	eid, err := pathInt(r, "enrollmentId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_enrollment_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	list, err := s.Students.ListStudentFeeBills(r.Context(), fid, eid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"bills": list})
}

func (s *Server) handleListAttendance(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	list, err := s.Students.ListAttendance(r.Context(), fid, date)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"attendance": list, "date": date})
}

func (s *Server) handleMarkAttendance(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in students.MarkAttendanceInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if in.MarkedBy == nil && ac.Principal != nil {
		uid := ac.Principal.UserID
		in.MarkedBy = &uid
	}
	if ac.Principal != nil {
		in.Role = ac.Principal.Role
	}
	result, err := s.Students.MarkAttendance(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Flat attendance fields for existing StrollX clients + push stats (PHP parity).
	out := map[string]interface{}{
		"enrollment_id":   result.Attendance.EnrollmentID,
		"student_id":      result.Attendance.StudentID,
		"facility_id":     result.Attendance.FacilityID,
		"first_name":      result.Attendance.FirstName,
		"last_name":       result.Attendance.LastName,
		"contact_phone":   result.Attendance.Phone,
		"plan_name":       result.Attendance.PlanName,
		"attendance_date": result.Attendance.AttendanceDate,
		"status":          result.Attendance.Status,
		"notes":           result.Attendance.Notes,
		"push":            result.Push,
	}
	if result.Attendance.AttendanceID != nil {
		out["attendance_id"] = *result.Attendance.AttendanceID
	}
	if result.Activated {
		out["activated"] = true
		out["current_status"] = "Active"
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	eid, err := pathInt(r, "enrollmentId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_enrollment_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in students.UpdateStatusInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Students.UpdateEnrollmentStatus(r.Context(), fid, eid, in.Status)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleUpdateStudent(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	sid, err := pathInt(r, "studentId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_student_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in students.UpdateStudentInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Students.UpdateStudent(r.Context(), fid, sid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleUpdatePlan(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	eid, err := pathInt(r, "enrollmentId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_enrollment_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in students.UpdatePlanInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Students.UpdatePlan(r.Context(), fid, eid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleSaveStudentPhoto(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	sid, err := pathInt(r, "studentId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_student_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var body struct {
		ImageBase64 string `json:"image_base64"`
		Ext         string `json:"ext"`
		Remove      bool   `json:"remove"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if body.Remove {
		if err := s.Students.ClearStudentPhoto(r.Context(), fid, sid); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"photo_url": ""})
		return
	}
	url, err := s.Students.SaveStudentPhoto(r.Context(), fid, sid, body.ImageBase64, body.Ext)
	if err != nil {
		status := http.StatusBadRequest
		if err.Error() == "student_not_found" {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"photo_url": url})
}

func (s *Server) handleStudentPhoto(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	name := strings.TrimSpace(r.PathValue("name"))
	idPart := name
	if i := strings.Index(name, "."); i > 0 {
		idPart = name[:i]
	}
	sid, err := strconv.ParseInt(idPart, 10, 64)
	if err != nil || sid <= 0 {
		http.NotFound(w, r)
		return
	}
	path, contentType, err := s.Students.StudentPhotoFile(r.Context(), sid, func(facilityID int64) bool {
		return s.requireFacility(ac, facilityID) == nil
	})
	if err != nil {
		if err.Error() == "forbidden_facility" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, path)
}

func (s *Server) handleListPlans(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	list, err := s.Students.ListPlans(r.Context(), fid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"plans": list})
}

func (s *Server) handleCreatePlan(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var body struct {
		PlanName    string  `json:"plan_name"`
		Name        string  `json:"name"`
		TotalFee    float64 `json:"total_fee"`
		DaysPerWeek int     `json:"days_per_week"`
		Description string  `json:"description"`
		StartTime    string `json:"start_time"`
		EndTime      string `json:"end_time"`
		CoachingDays string `json:"coaching_days"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	name := strings.TrimSpace(body.PlanName)
	if name == "" {
		name = strings.TrimSpace(body.Name)
	}
	row, err := s.Students.CreatePlan(r.Context(), fid, students.CreatePlanInput{
		PlanName:     name,
		TotalFee:     body.TotalFee,
		DaysPerWeek:  body.DaysPerWeek,
		Description:  body.Description,
		StartTime:    body.StartTime,
		EndTime:      body.EndTime,
		CoachingDays: body.CoachingDays,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleUpdateCoachingPlan(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	planID, err := pathInt(r, "planId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_plan_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var body struct {
		PlanName    string   `json:"plan_name"`
		Name        string   `json:"name"`
		TotalFee    *float64 `json:"total_fee"`
		DaysPerWeek *int     `json:"days_per_week"`
		Description *string  `json:"description"`
		StartTime    *string `json:"start_time"`
		EndTime      *string `json:"end_time"`
		CoachingDays *string `json:"coaching_days"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	name := strings.TrimSpace(body.PlanName)
	if name == "" {
		name = strings.TrimSpace(body.Name)
	}
	row, err := s.Students.UpdateCoachingPlan(r.Context(), fid, planID, students.UpdateCoachingPlanInput{
		PlanName:     name,
		TotalFee:     body.TotalFee,
		DaysPerWeek:  body.DaysPerWeek,
		Description:  body.Description,
		StartTime:    body.StartTime,
		EndTime:      body.EndTime,
		CoachingDays: body.CoachingDays,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleSoftDeleteCoachingPlan(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	planID, err := pathInt(r, "planId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_plan_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	planName := strings.TrimSpace(r.URL.Query().Get("plan_name"))
	if planName == "" {
		var body struct {
			PlanName string `json:"plan_name"`
			Name     string `json:"name"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
		planName = strings.TrimSpace(body.PlanName)
		if planName == "" {
			planName = strings.TrimSpace(body.Name)
		}
	}
	if err := s.Students.SoftDeleteCoachingPlan(r.Context(), fid, planID, planName); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":      true,
		"message": "Batch hidden. Students in this batch are inactive. Their records stay.",
	})
}

func (s *Server) handleGetAttendanceSettings(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	row, err := s.Students.GetAttendanceSettings(r.Context(), fid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "settings": row})
}

func (s *Server) handlePutAttendanceSettings(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	role := ""
	if ac.Principal != nil {
		role = ac.Principal.Role
	}
	role = strings.ToLower(strings.TrimSpace(role))

	var body map[string]interface{}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}

	in := students.UpdateAttendanceSettingsInput{}
	if raw, ok := body["batch_start_time"]; ok {
		// App Setup: admin + sub_admin
		if role != "admin" && role != "sub_admin" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "batch_time_admin_only"})
			return
		}
		sval := fmt.Sprint(raw)
		in.BatchStartTime = &sval
		in.UpdateBatch = true
	}
	if raw, ok := body["reminder_offset_minutes"]; ok {
		if role != "admin" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "reminder_admin_only"})
			return
		}
		n, _ := strconv.Atoi(fmt.Sprint(raw))
		in.ReminderOffsetMinutes = &n
		in.UpdateReminder = true
	}
	if raw, ok := body["attendance_radius_meters"]; ok {
		// Administration: admin only
		if role != "admin" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "radius_admin_only"})
			return
		}
		n, _ := strconv.Atoi(fmt.Sprint(raw))
		in.AttendanceRadiusMeters = &n
		in.UpdateRadius = true
	}
	_, hasLat := body["facility_latitude"]
	_, hasLng := body["facility_longitude"]
	if hasLat || hasLng {
		if role != "admin" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "coords_admin_only"})
			return
		}
		in.UpdateCoords = true
		if hasLat && body["facility_latitude"] != nil {
			v, _ := strconv.ParseFloat(fmt.Sprint(body["facility_latitude"]), 64)
			in.FacilityLatitude = &v
		}
		if hasLng && body["facility_longitude"] != nil {
			v, _ := strconv.ParseFloat(fmt.Sprint(body["facility_longitude"]), 64)
			in.FacilityLongitude = &v
		}
	}
	if !in.UpdateBatch && !in.UpdateRadius && !in.UpdateCoords && !in.UpdateReminder {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no_settings_fields"})
		return
	}
	row, err := s.Students.UpdateAttendanceSettings(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "settings": row})
}

func (s *Server) handleAttendanceAccess(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	uid := int64(0)
	role := ""
	if ac.Principal != nil {
		uid = ac.Principal.UserID
		role = ac.Principal.Role
	}
	if q := strings.TrimSpace(r.URL.Query().Get("coach_user_id")); q != "" {
		if v, e := strconv.ParseInt(q, 10, 64); e == nil && v > 0 {
			uid = v
		}
	}
	if uid <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id_required"})
		return
	}
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	access, err := s.Students.ResolveAttendanceAccess(r.Context(), fid, uid, role, date)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "access": access})
}

func (s *Server) handleSetCoachingLeave(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var body struct {
		Date       string `json:"date"`
		Note       string `json:"note"`
		PlanName   string `json:"plan_name"`
		AllBatches bool   `json:"all_batches"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	var actor *int64
	if ac.Principal != nil && ac.Principal.UserID > 0 {
		id := ac.Principal.UserID
		actor = &id
	}
	result, err := s.Students.SetCoachingLeave(r.Context(), fid, students.SetCoachingLeaveInput{
		Date:       body.Date,
		Note:       body.Note,
		PlanName:   body.PlanName,
		AllBatches: body.AllBatches,
		CreatedBy:  actor,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleListCoachingLeaves(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	planName := strings.TrimSpace(r.URL.Query().Get("plan_name"))
	list, err := s.Students.ListCoachingLeaves(r.Context(), fid, planName)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"leaves": list})
}

func (s *Server) handleCancelCoachingLeave(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var body struct {
		Date       string `json:"date"`
		PlanName   string `json:"plan_name"`
		AllBatches bool   `json:"all_batches"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	result, err := s.Students.CancelCoachingLeave(r.Context(), fid, students.SetCoachingLeaveInput{
		Date:       body.Date,
		PlanName:   body.PlanName,
		AllBatches: body.AllBatches,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func pathInt(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(r.PathValue(name), 10, 64)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
