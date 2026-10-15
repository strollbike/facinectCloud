package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/facinect/app/internal/app"
	"github.com/facinect/app/internal/authn"
	"github.com/facinect/app/internal/config"
)

type Server struct {
	Cfg config.Config
	App *app.Service
	JWT *authn.Verifier
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/app/health", s.handleHealth)

	// PHP-compatible consumer endpoints (POST action JSON).
	mux.HandleFunc("POST /v1/app/users_lookup", s.handleUsersLookup)
	mux.HandleFunc("GET /v1/app/facilities_nearby", s.handleFacilitiesNearby)
	mux.HandleFunc("POST /v1/app/facilities_nearby", s.handleFacilitiesNearby)
	mux.HandleFunc("GET /v1/app/get_slots", s.handleGetSlots)
	mux.HandleFunc("POST /v1/app/get_slots", s.handleGetSlots)
	mux.HandleFunc("POST /v1/app/mark_booked", s.handleMarkBooked)
	mux.HandleFunc("POST /v1/app/booking_checkout", s.handleBookingCheckout)
	mux.HandleFunc("POST /v1/app/my_bookings", s.handleMyBookings)
	mux.HandleFunc("POST /v1/app/student_portal", s.handleStudentPortal)
	mux.HandleFunc("POST /v1/app/primary_members", s.handlePrimaryMembers)
	mux.HandleFunc("POST /v1/app/billing_portal", s.handleBillingPortal)
	mux.HandleFunc("GET /v1/app/live_arena", s.handleLiveArena)
	mux.HandleFunc("POST /v1/app/live_arena", s.handleLiveArena)
	mux.HandleFunc("GET /v1/app/score_live", s.handleScoreLive)
	mux.HandleFunc("POST /v1/app/score_live", s.handleScoreLive)
	mux.HandleFunc("GET /v1/app/score_follows", s.handleScoreFollows)
	mux.HandleFunc("POST /v1/app/score_follows", s.handleScoreFollows)
	mux.HandleFunc("POST /v1/app/facility_feedback", s.handleFacilityFeedback)
	mux.HandleFunc("POST /v1/app/facility_customer_access", s.handleFacilityAccess)
	mux.HandleFunc("GET /v1/app/app_notifications", s.handleAppNotifications)
	mux.HandleFunc("POST /v1/app/app_notifications", s.handleAppNotifications)
	mux.HandleFunc("POST /v1/app/fcm_register", s.handleFCMRegister)
	mux.HandleFunc("POST /v1/app/news", s.handleNewsProxyHint)
	mux.HandleFunc("POST /v1/app/privacy", s.handlePrivacyProxyHint)
	mux.HandleFunc("GET /v1/app/geo_ip", s.handleGeoIP)
	mux.HandleFunc("POST /v1/app/geo_ip", s.handleGeoIP)

	return s.withCORS(mux)
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := s.Cfg.CORSOrigins
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Service-Key")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func phpOK(w http.ResponseWriter, ok bool, message string, data any, code int) {
	if code == 0 {
		if ok {
			code = http.StatusOK
		} else {
			code = http.StatusBadRequest
		}
	}
	writeJSON(w, code, map[string]any{
		"success": ok,
		"message": message,
		"data":    data,
	})
}

func (s *Server) readBody(r *http.Request) (map[string]any, error) {
	defer r.Body.Close()
	var body map[string]any
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(&body); err != nil {
		return map[string]any{}, err
	}
	if body == nil {
		body = map[string]any{}
	}
	return body, nil
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ok := true
	if err := s.App.DB.Ping(); err != nil {
		ok = false
	}
	status := "ok"
	if !ok {
		status = "degraded"
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "service": "app"})
}

func (s *Server) handleUsersLookup(w http.ResponseWriter, r *http.Request) {
	body, err := s.readBody(r)
	if err != nil {
		phpOK(w, false, "invalid_json", nil, 400)
		return
	}
	ok, msg, data := s.App.UsersLookup(r.Context(), body)
	phpOK(w, ok, msg, data, 0)
}

func (s *Server) handleFacilitiesNearby(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{}
	if r.Method == http.MethodPost {
		var err error
		body, err = s.readBody(r)
		if err != nil {
			phpOK(w, false, "invalid_json", nil, 400)
			return
		}
	}
	for k, v := range s.queryMap(r) {
		if _, exists := body[k]; !exists || fmtSprint(body[k]) == "" {
			body[k] = v
		}
	}
	ok, msg, data := s.App.FacilitiesNearby(r.Context(), body)
	phpOK(w, ok, msg, data, 0)
}

func (s *Server) handleGetSlots(w http.ResponseWriter, r *http.Request) {
	fid, _ := strconv.ParseInt(r.URL.Query().Get("facilityId"), 10, 64)
	sid, _ := strconv.ParseInt(r.URL.Query().Get("sportId"), 10, 64)
	date := r.URL.Query().Get("date")
	if r.Method == http.MethodPost {
		body, _ := s.readBody(r)
		if v, _ := strconv.ParseInt(fmt.Sprint(body["facilityId"]), 10, 64); v > 0 {
			fid = v
		}
		if v, _ := strconv.ParseInt(fmt.Sprint(body["sportId"]), 10, 64); v > 0 {
			sid = v
		}
		if d := strings.TrimSpace(fmt.Sprint(body["date"])); d != "" && d != "<nil>" {
			date = d
		}
	}
	ok, msg, data := s.App.GetSlots(r.Context(), fid, sid, date)
	phpOK(w, ok, msg, data, 0)
}

func fmtSprint(v any) string {
	if v == nil {
		return ""
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "<nil>" {
		return ""
	}
	return s
}

func (s *Server) handleMarkBooked(w http.ResponseWriter, r *http.Request) {
	body, err := s.readBody(r)
	if err != nil {
		phpOK(w, false, "invalid_json", nil, 400)
		return
	}
	ok, msg, data := s.App.MarkBooked(r.Context(), body)
	phpOK(w, ok, msg, data, 0)
}

func (s *Server) handleBookingCheckout(w http.ResponseWriter, r *http.Request) {
	body, err := s.readBody(r)
	if err != nil {
		phpOK(w, false, "invalid_json", nil, 400)
		return
	}
	ok, msg, data, code := s.App.BookingCheckout(r.Context(), body)
	phpOK(w, ok, msg, data, code)
}

func (s *Server) handleMyBookings(w http.ResponseWriter, r *http.Request) {
	body, err := s.readBody(r)
	if err != nil {
		phpOK(w, false, "invalid_json", nil, 400)
		return
	}
	ok, msg, data := s.App.MyBookings(r.Context(), body)
	phpOK(w, ok, msg, data, 0)
}

func (s *Server) handleStudentPortal(w http.ResponseWriter, r *http.Request) {
	body, err := s.readBody(r)
	if err != nil {
		phpOK(w, false, "invalid_json", nil, 400)
		return
	}
	ok, msg, data := s.App.StudentPortal(r.Context(), body)
	phpOK(w, ok, msg, data, 0)
}

func (s *Server) handlePrimaryMembers(w http.ResponseWriter, r *http.Request) {
	body, err := s.readBody(r)
	if err != nil {
		phpOK(w, false, "invalid_json", nil, 400)
		return
	}
	ok, msg, data := s.App.PrimaryMembers(r.Context(), body)
	phpOK(w, ok, msg, data, 0)
}

func (s *Server) handleBillingPortal(w http.ResponseWriter, r *http.Request) {
	body, err := s.readBody(r)
	if err != nil {
		phpOK(w, false, "invalid_json", nil, 400)
		return
	}
	ok, msg, data := s.App.BillingPortal(r.Context(), body)
	phpOK(w, ok, msg, data, 0)
}

func (s *Server) queryMap(r *http.Request) map[string]string {
	out := map[string]string{}
	for k, vals := range r.URL.Query() {
		if len(vals) > 0 {
			out[k] = vals[0]
		}
	}
	return out
}

func (s *Server) handleLiveArena(w http.ResponseWriter, r *http.Request) {
	q := s.queryMap(r)
	action := q["action"]
	if r.Method == http.MethodPost {
		body, _ := s.readBody(r)
		if a := strings.TrimSpace(fmtSprint(body["action"])); a != "" {
			action = a
		}
		for k, v := range body {
			if _, ok := q[k]; !ok {
				q[k] = fmtSprint(v)
			}
		}
	}
	ok, msg, data := s.App.LiveArena(r.Context(), action, q)
	phpOK(w, ok, msg, data, 0)
}

func (s *Server) handleScoreLive(w http.ResponseWriter, r *http.Request) {
	q := s.queryMap(r)
	action := q["action"]
	if r.Method == http.MethodPost {
		body, _ := s.readBody(r)
		if a := strings.TrimSpace(fmtSprint(body["action"])); a != "" {
			action = a
		}
		for k, v := range body {
			q[k] = fmtSprint(v)
		}
	}
	ok, msg, data := s.App.ScoreLive(r.Context(), action, q)
	phpOK(w, ok, msg, data, 0)
}

func (s *Server) handleScoreFollows(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{}
	if r.Method == http.MethodPost {
		var err error
		body, err = s.readBody(r)
		if err != nil {
			phpOK(w, false, "invalid_json", nil, 400)
			return
		}
	}
	for k, v := range s.queryMap(r) {
		if _, exists := body[k]; !exists || fmtSprint(body[k]) == "" {
			body[k] = v
		}
	}
	ok, msg, data := s.App.ScoreFollows(r.Context(), body)
	phpOK(w, ok, msg, data, 0)
}

func (s *Server) handleFacilityFeedback(w http.ResponseWriter, r *http.Request) {
	body, err := s.readBody(r)
	if err != nil {
		phpOK(w, false, "invalid_json", nil, 400)
		return
	}
	ok, msg, data := s.App.FacilityFeedback(r.Context(), body)
	phpOK(w, ok, msg, data, 0)
}

func (s *Server) handleFacilityAccess(w http.ResponseWriter, r *http.Request) {
	body, err := s.readBody(r)
	if err != nil {
		phpOK(w, false, "invalid_json", nil, 400)
		return
	}
	ok, msg, data, code := s.App.FacilityAccess(r.Context(), body)
	phpOK(w, ok, msg, data, code)
}

func (s *Server) handleAppNotifications(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{}
	if r.Method == http.MethodPost {
		var err error
		body, err = s.readBody(r)
		if err != nil {
			phpOK(w, false, "invalid_json", nil, 400)
			return
		}
	}
	for k, v := range s.queryMap(r) {
		if _, exists := body[k]; !exists || fmtSprint(body[k]) == "" {
			body[k] = v
		}
	}
	ok, msg, data := s.App.AppNotifications(r.Context(), body)
	phpOK(w, ok, msg, data, 0)
}

func (s *Server) handleFCMRegister(w http.ResponseWriter, r *http.Request) {
	body, err := s.readBody(r)
	if err != nil {
		phpOK(w, false, "invalid_json", nil, 400)
		return
	}
	ok, msg, data := s.App.FCMRegister(r.Context(), body)
	phpOK(w, ok, msg, data, 0)
}

func (s *Server) handleNewsProxyHint(w http.ResponseWriter, r *http.Request) {
	phpOK(w, true, "use /v1/news", map[string]any{"items": []any{}}, 200)
}

func (s *Server) handlePrivacyProxyHint(w http.ResponseWriter, r *http.Request) {
	phpOK(w, true, "use /v1/privacy", map[string]any{}, 200)
}

func (s *Server) handleGeoIP(w http.ResponseWriter, r *http.Request) {
	ip := app.ClientIPFromRequest(r)
	if r.Method == http.MethodPost {
		body, _ := s.readBody(r)
		if v := strings.TrimSpace(fmtSprint(body["ip"])); v != "" {
			ip = v
		}
	}
	if q := strings.TrimSpace(r.URL.Query().Get("ip")); q != "" {
		ip = q
	}
	res := s.App.ResolveGeoIP(r.Context(), ip)
	if !res.OK {
		phpOK(w, false, res.Message, map[string]any{
			"source":  res.Source,
			"queryIp": res.QueryIP,
		}, 200)
		return
	}
	phpOK(w, true, "OK", map[string]any{
		"latitude":  res.Latitude,
		"longitude": res.Longitude,
		"city":      res.City,
		"region":    res.Region,
		"country":   res.Country,
		"queryIp":   res.QueryIP,
		"source":    res.Source,
		"approx":    true,
	}, 200)
}
