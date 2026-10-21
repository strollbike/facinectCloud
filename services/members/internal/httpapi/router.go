package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/facinect/members/internal/authn"
	"github.com/facinect/members/internal/config"
	"github.com/facinect/members/internal/members"
)

type Server struct {
	Cfg     config.Config
	Members *members.Service
	JWT     *authn.Verifier
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/members/health", s.handleHealth)
	mux.HandleFunc("GET /v1/members/openapi.json", s.handleOpenAPI)

	mux.HandleFunc("GET /v1/members/facilities/{facilityId}/members/summary", s.withAuth(s.handleSummary))
	mux.HandleFunc("GET /v1/members/facilities/{facilityId}/members", s.withAuth(s.handleListMembers))
	mux.HandleFunc("POST /v1/members/facilities/{facilityId}/members", s.withAuth(s.handleRegister))
	mux.HandleFunc("POST /v1/members/facilities/{facilityId}/memberships/{membershipId}/status", s.withAuth(s.handleUpdateStatus))
	mux.HandleFunc("POST /v1/members/facilities/{facilityId}/memberships/{membershipId}", s.withAuth(s.handleUpdateProfile))
	mux.HandleFunc("PUT /v1/members/facilities/{facilityId}/memberships/{membershipId}", s.withAuth(s.handleUpdateProfile))

	mux.HandleFunc("GET /v1/members/facilities/{facilityId}/plans", s.withAuth(s.handleListPlans))
	mux.HandleFunc("POST /v1/members/facilities/{facilityId}/plans", s.withAuth(s.handleCreatePlan))
	mux.HandleFunc("DELETE /v1/members/facilities/{facilityId}/plans/{planId}", s.withAuth(s.handleDeletePlan))
	mux.HandleFunc("POST /v1/members/facilities/{facilityId}/plans/{planId}/delete", s.withAuth(s.handleDeletePlan))
	mux.HandleFunc("GET /v1/members/facilities/{facilityId}/plans/{planId}/settings", s.withAuth(s.handleGetPlanSettings))
	mux.HandleFunc("PUT /v1/members/facilities/{facilityId}/plans/{planId}/settings", s.withAuth(s.handlePutPlanSettings))
	mux.HandleFunc("POST /v1/members/facilities/{facilityId}/plans/{planId}/settings", s.withAuth(s.handlePutPlanSettings))
	mux.HandleFunc("POST /v1/members/facilities/{facilityId}/plans/{planId}/team", s.withAuth(s.handlePlanTeam))
	mux.HandleFunc("POST /v1/members/facilities/{facilityId}/bulk-group", s.withAuth(s.handleBulkGroup))
	mux.HandleFunc("POST /v1/members/facilities/{facilityId}/coupons", s.withAuth(s.handleGenerateCoupons))
	mux.HandleFunc("POST /v1/members/facilities/{facilityId}/bulk-notify", s.withAuth(s.handleBulkNotify))
	mux.HandleFunc("POST /v1/members/facilities/{facilityId}/payment-reminders", s.withAuth(s.handlePaymentReminders))
	mux.HandleFunc("GET /v1/members/facilities/{facilityId}/payment-reminder-schedule", s.withAuth(s.handleGetPaymentReminderSchedule))
	mux.HandleFunc("PUT /v1/members/facilities/{facilityId}/payment-reminder-schedule", s.withAuth(s.handlePutPaymentReminderSchedule))
	mux.HandleFunc("POST /v1/members/facilities/{facilityId}/payment-reminder-schedule", s.withAuth(s.handlePutPaymentReminderSchedule))
	mux.HandleFunc("GET /v1/members/facilities/{facilityId}/open-slots", s.withAuth(s.handleListOpenSlots))
	mux.HandleFunc("POST /v1/members/facilities/{facilityId}/open-slots", s.withAuth(s.handleCreateOpenSlot))
	mux.HandleFunc("POST /v1/members/memberships/{id}/payment-status", s.withAuth(s.handlePaymentStatus))
	mux.HandleFunc("POST /v1/members/facilities/{facilityId}/bulk-payment", s.withAuth(s.handleBulkPayment))
	mux.HandleFunc("POST /v1/members/facilities/{facilityId}/change-plan", s.withAuth(s.handleChangePlan))
	mux.HandleFunc("GET /v1/members/facilities/{facilityId}/requests", s.withAuth(s.handleListRequests))
	mux.HandleFunc("POST /v1/members/requests/{id}/decide", s.withAuth(s.handleDecideRequest))

	return s.withCORS(mux)
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := s.corsOrigin(); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Service-Key")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
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
	if err := s.Members.DB.Ping(); err != nil {
		ok = false
	}
	status := http.StatusOK
	st := "ok"
	if !ok {
		status = http.StatusServiceUnavailable
		st = "degraded"
	}
	writeJSON(w, status, map[string]interface{}{
		"service":         "members",
		"status":          st,
		"postgres":        ok,
		"service_key_set": s.Cfg.ServiceKey != "",
		"jwt_ready":       s.JWT != nil,
		"runtime":         "go",
	})
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request, ac *authCtx) {
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
	list, err := s.Members.ListMembers(r.Context(), fid, sportID, status)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"members": list})
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
	n, err := s.Members.CountActive(r.Context(), fid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"active_count": n})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in members.RegisterInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Members.Register(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	mid, err := pathInt(r, "membershipId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_membership_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in members.UpdateStatusInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Members.UpdateMembershipStatus(r.Context(), fid, mid, in.Status)
	if err != nil {
		code := http.StatusBadRequest
		if err.Error() == "not_found" {
			code = http.StatusNotFound
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	mid, err := pathInt(r, "membershipId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_membership_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in members.UpdateProfileInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Members.UpdateMembershipProfile(r.Context(), fid, mid, in)
	if err != nil {
		code := http.StatusBadRequest
		switch err.Error() {
		case "not_found":
			code = http.StatusNotFound
		case "facility_mismatch":
			code = http.StatusForbidden
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
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
	list, err := s.Members.ListPlans(r.Context(), fid)
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
	var in members.CreatePlanInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	p, err := s.Members.CreatePlan(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeletePlan(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	pid, err := pathInt(r, "planId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_plan_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	planName, err := s.Members.DeletePlan(r.Context(), fid, pid)
	if err != nil {
		code := http.StatusBadRequest
		if err.Error() == "not_found" {
			code = http.StatusNotFound
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":   true,
		"plan_name": planName,
	})
}

func (s *Server) handleGetPlanSettings(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	pid, err := pathInt(r, "planId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_plan_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	ps, err := s.Members.GetPlanSettings(r.Context(), fid, pid)
	if err != nil {
		code := http.StatusBadRequest
		if err.Error() == "not_found" {
			code = http.StatusNotFound
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ps)
}

func (s *Server) handlePutPlanSettings(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	pid, err := pathInt(r, "planId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_plan_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var body map[string]interface{}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	settings := body
	if nested, ok := body["settings"].(map[string]interface{}); ok {
		settings = nested
	}
	ps, err := s.Members.UpsertPlanSettings(r.Context(), fid, pid, settings)
	if err != nil {
		code := http.StatusBadRequest
		if err.Error() == "not_found" {
			code = http.StatusNotFound
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ps)
}

func (s *Server) handlePlanTeam(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	pid, err := pathInt(r, "planId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_plan_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in members.TeamInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	n, err := s.Members.UpdatePlanTeam(r.Context(), fid, pid, in)
	if err != nil {
		code := http.StatusBadRequest
		if err.Error() == "not_found" {
			code = http.StatusNotFound
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "updated": n})
}

func (s *Server) handleBulkGroup(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in members.BulkGroupInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	n, err := s.Members.BulkUpdateGroup(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "updated": n})
}

func (s *Server) handleGenerateCoupons(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in members.GenerateCouponsInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	list, err := s.Members.GenerateCoupons(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "coupons": list})
}

func (s *Server) handleBulkNotify(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in members.BulkNotifyInput
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	result, err := s.Members.BulkNotify(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":    true,
		"queued":     result.Queued,
		"whatsapp":   result.WhatsApp,
		"fcmPush":    result.FcmPush,
		"appPush":    result.AppPush,
		"noAppUser":  result.NoAppUser,
		"noFcmToken": result.NoFcmToken,
		"message":    result.Message,
	})
}

func (s *Server) handlePaymentReminders(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in members.PaymentReminderInput
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	if strings.TrimSpace(in.Audience) == "" {
		in.Audience = "member"
	}
	result, err := s.Members.SendPaymentReminders(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":   true,
		"queued":    result.Queued,
		"whatsapp":  result.WhatsApp,
		"fcmPush":   result.FcmPush,
		"appPush":   result.AppPush,
		"noAppUser": result.NoAppUser,
		"message":   result.Message,
	})
}

func (s *Server) handleGetPaymentReminderSchedule(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	audience := strings.TrimSpace(r.URL.Query().Get("audience"))
	if audience == "" {
		audience = "member"
	}
	sch, err := s.Members.GetPaymentReminderSchedule(r.Context(), fid, audience)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "schedule": sch})
}

func (s *Server) handlePutPaymentReminderSchedule(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in members.PaymentReminderSchedule
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	if strings.TrimSpace(in.Audience) == "" {
		in.Audience = strings.TrimSpace(r.URL.Query().Get("audience"))
	}
	sch, err := s.Members.SetPaymentReminderSchedule(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "schedule": sch})
}

func (s *Server) handleListOpenSlots(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	list, err := s.Members.ListOpenSlots(r.Context(), fid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"slots": list})
}

func (s *Server) handleCreateOpenSlot(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in members.OpenSlotInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	slot, err := s.Members.CreateOpenSlot(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, slot)
}

func (s *Server) handlePaymentStatus(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_membership_id"})
		return
	}
	row, err := s.Members.GetMembership(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	if err := s.requireFacility(ac, row.FacilityID); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in members.PaymentStatusInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	st := strings.TrimSpace(in.PaymentStatus)
	if st == "" && in.Paid != nil {
		if *in.Paid {
			st = "paid"
		} else {
			st = "unpaid"
		}
	}
	updated, err := s.Members.UpdatePaymentStatus(r.Context(), id, st)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleBulkPayment(w http.ResponseWriter, r *http.Request, ac *authCtx) {
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
		PlanID int64 `json:"plan_id"`
		Paid   bool  `json:"paid"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	n, err := s.Members.BulkUpdatePayment(r.Context(), fid, body.PlanID, body.Paid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "updated": n})
}

func (s *Server) handleChangePlan(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in members.ChangePlanInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	n, err := s.Members.ChangeOrAddPlan(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "updated": n})
}

func (s *Server) handleListRequests(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	list, err := s.Members.ListRequests(r.Context(), fid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"requests": list})
}

func (s *Server) handleDecideRequest(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request_id"})
		return
	}
	existing, err := s.Members.GetRequest(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	if err := s.requireFacility(ac, existing.FacilityID); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in members.DecideRequestInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	req, err := s.Members.DecideRequest(r.Context(), id, in.Decision)
	if err != nil {
		code := http.StatusBadRequest
		if err.Error() == "not_found" {
			code = http.StatusNotFound
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func pathInt(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(r.PathValue(name), 10, 64)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
