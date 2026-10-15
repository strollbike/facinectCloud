package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/facinect/booking/internal/authn"
	"github.com/facinect/booking/internal/booking"
	"github.com/facinect/booking/internal/config"
)

type Server struct {
	Cfg    config.Config
	Book   *booking.Service
	JWT    *authn.Verifier
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/booking/health", s.handleHealth)
	mux.HandleFunc("GET /v1/booking/openapi.json", s.handleOpenAPI)

	mux.HandleFunc("GET /v1/booking/facilities/{facilityId}/courts", s.withAuth(s.handleListCourts))
	mux.HandleFunc("POST /v1/booking/facilities/{facilityId}/courts", s.withAuth(s.handleCreateCourt))

	mux.HandleFunc("GET /v1/booking/facilities/{facilityId}/slots", s.withAuth(s.handleListSlots))
	mux.HandleFunc("POST /v1/booking/facilities/{facilityId}/slots/generate", s.withAuth(s.handleGenerateSlots))
	mux.HandleFunc("POST /v1/booking/slots/generate-all", s.withAuth(s.handleGenerateAllSlots))
	mux.HandleFunc("POST /v1/booking/slots/maintenance", s.withAuth(s.handleSlotMaintenance))
	mux.HandleFunc("POST /v1/booking/slots/{slotId}/block", s.withAuth(s.handleBlockSlot))
	mux.HandleFunc("POST /v1/booking/slots/{slotId}/unblock", s.withAuth(s.handleUnblockSlot))

	mux.HandleFunc("GET /v1/booking/facilities/{facilityId}/bookings", s.withAuth(s.handleListBookings))
	mux.HandleFunc("POST /v1/booking/facilities/{facilityId}/bookings", s.withAuth(s.handleCreateBooking))
	mux.HandleFunc("GET /v1/booking/bookings/{bookingId}", s.withAuth(s.handleGetBooking))
	mux.HandleFunc("POST /v1/booking/bookings/{bookingId}/cancel", s.withAuth(s.handleCancelBooking))

	mux.HandleFunc("POST /v1/booking/facilities/{facilityId}/slots/cleanup", s.withAuth(s.handleCleanupSlots))
	mux.HandleFunc("GET /v1/booking/facilities/{facilityId}/blocks", s.withAuth(s.handleListBlocks))
	mux.HandleFunc("POST /v1/booking/facilities/{facilityId}/blocks/{blockId}/delete", s.withAuth(s.handleDeleteBlockRule))
	mux.HandleFunc("GET /v1/booking/facilities/{facilityId}/blocks/{blockId}/dates", s.withAuth(s.handleBlockRuleDates))
	mux.HandleFunc("POST /v1/booking/facilities/{facilityId}/blocks/{blockId}/free-range", s.withAuth(s.handleFreeBlockRuleRange))
	mux.HandleFunc("POST /v1/booking/facilities/{facilityId}/blocks/free-by-reason", s.withAuth(s.handleFreeBlocksByReason))
	mux.HandleFunc("POST /v1/booking/facilities/{facilityId}/blocks/recurrence", s.withAuth(s.handleRecurrenceBlocks))
	mux.HandleFunc("POST /v1/booking/bookings/{bookingId}/collect", s.withAuth(s.handleCollectPayment))
	mux.HandleFunc("POST /v1/booking/facilities/{facilityId}/coupons", s.withAuth(s.handleCreateCoupons))
	mux.HandleFunc("POST /v1/booking/users/{userId}/status", s.withAuth(s.handleUserStatus))
	mux.HandleFunc("POST /v1/booking/facilities/{facilityId}/users/{userId}/status", s.withAuth(s.handleFacilityUserStatus))

	// Optional payment-link booking: hold 30 min → pay → auto book.
	mux.HandleFunc("POST /v1/booking/facilities/{facilityId}/payment-holds", s.withAuth(s.handleCreatePaymentHold))
	mux.HandleFunc("POST /v1/booking/facilities/{facilityId}/payment-holds/{holdId}/confirm", s.withAuth(s.handleConfirmPaymentHoldByID))
	mux.HandleFunc("GET /v1/booking/pay/{token}", s.handlePayPage)          // public
	mux.HandleFunc("POST /v1/booking/pay/{token}/confirm", s.handlePayConfirm) // public (stub pay / webhook-style)

	return s.withCORS(mux)
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := s.corsOrigin(); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Service-Key")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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
	if s.Cfg.ServiceKey != "" && key == "" && s.JWT == nil {
		return nil, errUnauthorized
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
	if err := s.Book.DB.Ping(); err != nil {
		ok = false
	}
	status := http.StatusOK
	st := "ok"
	if !ok {
		status = http.StatusServiceUnavailable
		st = "degraded"
	}
	writeJSON(w, status, map[string]interface{}{
		"service":         "booking",
		"status":          st,
		"postgres":        ok,
		"service_key_set": s.Cfg.ServiceKey != "",
		"jwt_ready":       s.JWT != nil,
		"runtime":         "go",
	})
}

func (s *Server) handleListCourts(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	list, err := s.Book.ListCourts(r.Context(), fid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"courts": list})
}

func (s *Server) handleCreateCourt(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in booking.CreateCourtInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	c, err := s.Book.CreateCourt(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleListSlots(w http.ResponseWriter, r *http.Request, ac *authCtx) {
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
	var courtID *int64
	if v := strings.TrimSpace(r.URL.Query().Get("court_id")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_court_id"})
			return
		}
		courtID = &n
	}
	list, err := s.Book.ListSlots(r.Context(), fid, date, courtID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"slots": list})
}

func (s *Server) handleGenerateSlots(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in booking.GenerateSlotsInput
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	n, err := s.Book.GenerateSlots(r.Context(), fid, in)
	if err != nil {
		msg := err.Error()
		code := http.StatusBadRequest
		if strings.HasPrefix(msg, "conflict:") {
			code = http.StatusConflict
		}
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	from, to := booking.MonthWindow(time.Now())
	out := map[string]interface{}{"created": n}
	if in.UseMonthWindowPublic() {
		out["from"] = from.Format("2006-01-02")
		out["to"] = to.Format("2006-01-02")
		out["month_window"] = true
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGenerateAllSlots — PHP GenerateAllFacilitiesAvailability_v1 parity.
func (s *Server) handleGenerateAllSlots(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	// Prefer service key for fleet-wide jobs; JWT users allowed in local.
	if !ac.ViaKey && !s.Cfg.IsLocal() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "service_key_required"})
		return
	}
	result, err := s.Book.GenerateAllFacilitiesMonthWindow(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleSlotMaintenance — clean past/previous-month unused slots + regenerate month window.
func (s *Server) handleSlotMaintenance(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	if !ac.ViaKey && !s.Cfg.IsLocal() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "service_key_required"})
		return
	}
	var body struct {
		ForceMonthEnd bool `json:"force_month_end"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	result, err := s.Book.RunSlotMaintenance(r.Context(), time.Now(), body.ForceMonthEnd)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleBlockSlot(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	s.mutateSlot(w, r, ac, true)
}

func (s *Server) handleUnblockSlot(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	s.mutateSlot(w, r, ac, false)
}

func (s *Server) mutateSlot(w http.ResponseWriter, r *http.Request, ac *authCtx, block bool) {
	slotID, err := pathInt(r, "slotId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_slot_id"})
		return
	}
	slot, err := s.Book.GetSlot(r.Context(), slotID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if err := s.requireFacility(ac, slot.FacilityID); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	notes := ""
	if block {
		var body struct {
			Notes string `json:"notes"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
		notes = body.Notes
		slot, err = s.Book.BlockSlot(r.Context(), slotID, notes)
	} else {
		slot, err = s.Book.UnblockSlot(r.Context(), slotID)
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, slot)
}

func (s *Server) handleListBookings(w http.ResponseWriter, r *http.Request, ac *authCtx) {
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
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	year, _ := strconv.Atoi(r.URL.Query().Get("year"))
	month, _ := strconv.Atoi(r.URL.Query().Get("month"))
	list, err := s.Book.ListBookings(r.Context(), fid, date, status, year, month)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"bookings": list})
}

func (s *Server) handleCreateBooking(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in booking.CreateBookingInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if in.UserID == nil && ac.Principal != nil {
		uid := ac.Principal.UserID
		in.UserID = &uid
	}
	b, err := s.Book.CreateBooking(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleGetBooking(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	id, err := pathInt(r, "bookingId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_booking_id"})
		return
	}
	b, err := s.Book.GetBooking(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	if err := s.requireFacility(ac, b.FacilityID); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleCancelBooking(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	id, err := pathInt(r, "bookingId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_booking_id"})
		return
	}
	b, err := s.Book.GetBooking(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	if err := s.requireFacility(ac, b.FacilityID); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	b, err = s.Book.CancelBooking(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleCleanupSlots(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	n, err := s.Book.CleanupSlots(r.Context(), fid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "deleted": n})
}

func (s *Server) handleListBlocks(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	// Default: plan/rule summary (PHP Active blocks). ?view=slots for every slot.
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("view")), "slots") {
		now := time.Now()
		from := strings.TrimSpace(r.URL.Query().Get("from"))
		to := strings.TrimSpace(r.URL.Query().Get("to"))
		if from == "" {
			from = now.Format("2006-01-02")
		}
		if to == "" {
			to = now.AddDate(1, 0, 0).Format("2006-01-02")
		}
		list, err := s.Book.ListFacilityBlocks(r.Context(), fid, from, to)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if list == nil {
			list = []booking.BlockListItem{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"blocks": list,
			"view":   "slots",
			"from":   from,
			"to":     to,
		})
		return
	}
	rules, err := s.Book.ListFacilityBlockRules(r.Context(), fid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if rules == nil {
		rules = []booking.BlockRuleItem{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"blocks": rules,
		"view":   "rules",
	})
}

func (s *Server) handleDeleteBlockRule(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	bid, err := pathInt(r, "blockId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_block_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var body struct {
		Kind string `json:"kind"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	n, err := s.Book.DeleteBlockRule(r.Context(), fid, bid, body.Kind)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "unblocked": n})
}

func (s *Server) handleBlockRuleDates(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	bid, err := pathInt(r, "blockId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_block_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	dates, err := s.Book.ListBlockRuleDates(r.Context(), fid, bid, kind)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	from, to := "", ""
	if len(dates) > 0 {
		from = dates[0]
		to = dates[len(dates)-1]
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"dates":   dates,
		"from":    from,
		"to":      to,
		"count":   len(dates),
	})
}

func (s *Server) handleFreeBlockRuleRange(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	bid, err := pathInt(r, "blockId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_block_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var body struct {
		Kind string `json:"kind"`
		From string `json:"from"`
		To   string `json:"to"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	n, err := s.Book.FreeBlockRuleRange(r.Context(), fid, bid, body.Kind, body.From, body.To)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "unblocked": n})
}

func (s *Server) handleFreeBlocksByReason(w http.ResponseWriter, r *http.Request, ac *authCtx) {
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
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	n, err := s.Book.FreeBlocksByReason(r.Context(), fid, body.Reason)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "unblocked": n})
}

func (s *Server) handleRecurrenceBlocks(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in booking.RecurrenceBlockInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	n, err := s.Book.CreateRecurrenceBlocks(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "blocked": n})
}

func (s *Server) handleCollectPayment(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	id, err := pathInt(r, "bookingId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_booking_id"})
		return
	}
	b, err := s.Book.GetBooking(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	if err := s.requireFacility(ac, b.FacilityID); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in booking.CollectPaymentInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	b, err = s.Book.CollectPayment(r.Context(), id, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleCreateCoupons(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in booking.CreateCouponsInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	list, err := s.Book.CreateCoupons(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "coupons": list})
}

func (s *Server) handleUserStatus(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	uid, err := pathInt(r, "userId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_user_id"})
		return
	}
	var body struct {
		booking.UserStatusInput
		FacilityID int64 `json:"facility_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if body.FacilityID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "facility_id_required"})
		return
	}
	if err := s.requireFacility(ac, body.FacilityID); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	if err := s.Book.UpdateUserStatus(r.Context(), body.FacilityID, uid, body.UserStatusInput); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "user_id": uid, "status": body.Status})
}

func (s *Server) handleFacilityUserStatus(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	uid, err := pathInt(r, "userId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_user_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in booking.UserStatusInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if err := s.Book.UpdateUserStatus(r.Context(), fid, uid, in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "user_id": uid, "status": in.Status})
}

func (s *Server) handleCreatePaymentHold(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in booking.CreatePaymentHoldInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if strings.TrimSpace(in.PublicBaseURL) == "" {
		// Prefer gateway host from request when client omits it.
		scheme := "http"
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		host := r.Header.Get("X-Forwarded-Host")
		if host == "" {
			host = r.Host
		}
		if host != "" {
			in.PublicBaseURL = scheme + "://" + host
		}
	}
	h, err := s.Book.CreatePaymentHold(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "hold": h})
}

func (s *Server) handleConfirmPaymentHoldByID(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	holdID, err := pathInt(r, "holdId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_hold_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	h, err := s.Book.GetPaymentHold(r.Context(), fid, holdID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	out, err := s.Book.ConfirmPaymentHold(r.Context(), h.PublicToken)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "hold": out})
}

func (s *Server) handlePayPage(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.PathValue("token"))
	h, err := s.Book.GetPaymentHoldByToken(r.Context(), token)
	if err != nil {
		http.Error(w, "Payment link not found", http.StatusNotFound)
		return
	}
	// Auto-invalidate after 30 minutes (even before the background ticker runs).
	if h.Status == "pending" {
		exp := booking.ParseHoldExpiresAt(h.ExpiresAt)
		if !exp.IsZero() && time.Now().After(exp) {
			_, _ = s.Book.ExpirePaymentHold(r.Context(), h.ID)
			h, err = s.Book.GetPaymentHoldByToken(r.Context(), token)
			if err != nil {
				http.Error(w, "Payment link not found", http.StatusNotFound)
				return
			}
		}
	}
	// If we have a Razorpay short URL, send the user there directly.
	if h.Status == "pending" && strings.HasPrefix(h.PaymentURL, "http") &&
		!strings.Contains(h.PaymentURL, "/v1/booking/pay/") {
		http.Redirect(w, r, h.PaymentURL, http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	amtType := h.AmountType
	if amtType == "" {
		amtType = "advance"
	}
	statusNote := ""
	switch h.Status {
	case "paid":
		statusNote = "<p style='color:#16a34a;font-weight:700'>Already paid — booking confirmed.</p>"
	case "expired", "cancelled":
		statusNote = "<p style='color:#ef4444;font-weight:700'>This link expired after 30 minutes and is no longer valid.</p>"
	default:
		statusNote = "<p style='color:#b45309'>Slots are held for 30 minutes. Complete payment to confirm.</p>"
	}
	payBtn := ""
	if h.Status == "pending" {
		payBtn = `<form method="POST" action="/v1/booking/pay/` + h.PublicToken + `/confirm">
  <button type="submit" style="background:#0d6efd;color:#fff;border:0;padding:12px 20px;border-radius:8px;font-weight:700;cursor:pointer">
    Pay ₹` + formatMoney(h.PayAmount) + ` (` + amtType + `)
  </button>
</form>`
	}
	logo := ""
	if h.LogoURL != "" {
		logo = `<img src="` + htmlEscape(h.LogoURL) + `" alt="logo" style="max-height:64px;margin-bottom:12px"/>`
	}
	_, _ = w.Write([]byte(`<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"/>
<title>Pay booking</title></head><body style="font-family:system-ui,sans-serif;max-width:480px;margin:40px auto;padding:0 16px">
` + logo + `
<h2>Booking payment</h2>
` + statusNote + `
<p><b>Sport:</b> ` + htmlEscape(h.SportName) + `<br/>
<b>Category:</b> ` + htmlEscape(h.Category) + `<br/>
<b>Slots:</b> ` + htmlEscape(h.SlotsSummary) + `<br/>
<b>Full amount:</b> ₹` + formatMoney(h.FullAmount) + `<br/>
<b>Pay now:</b> ₹` + formatMoney(h.PayAmount) + ` (` + htmlEscape(amtType) + `)<br/>
<b>Expires:</b> ` + htmlEscape(h.ExpiresAt) + `</p>
` + payBtn + `
<p style="color:#64748b;font-size:12px;margin-top:24px">Facinect booking hold</p>
</body></html>`))
}

func (s *Server) handlePayConfirm(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.PathValue("token"))
	// Razorpay payment-link callback may append ?razorpay_payment_link_status=paid
	st := strings.ToLower(r.URL.Query().Get("razorpay_payment_link_status"))
	if st != "" && st != "paid" {
		http.Error(w, "payment_not_paid", http.StatusBadRequest)
		return
	}
	out, err := s.Book.ConfirmPaymentHold(r.Context(), token)
	if err != nil {
		if strings.Contains(r.Header.Get("Accept"), "application/json") || r.Header.Get("Content-Type") == "application/json" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") || r.URL.Query().Get("format") == "json" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "hold": out})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><html><body style="font-family:system-ui;max-width:480px;margin:40px auto;padding:16px">
<h2 style="color:#16a34a">Payment received</h2>
<p>Your slots are booked. A confirmation will be sent on WhatsApp.</p>
<p><b>Slots:</b> ` + htmlEscape(out.SlotsSummary) + `</p>
</body></html>`))
}

func formatMoney(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}

func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
	return r.Replace(s)
}

func pathInt(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(r.PathValue(name), 10, 64)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
