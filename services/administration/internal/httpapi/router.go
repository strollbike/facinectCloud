package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/facinect/administration/internal/administration"
	"github.com/facinect/administration/internal/authn"
	"github.com/facinect/administration/internal/config"
)

type Server struct {
	Cfg   config.Config
	Admin *administration.Service
	JWT   *authn.Verifier
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/administration/health", s.handleHealth)
	mux.HandleFunc("GET /v1/administration/openapi.json", s.handleOpenAPI)
	mux.HandleFunc("GET /v1/administration/pages", s.withAuth(s.handlePages))

	mux.HandleFunc("GET /v1/administration/facilities/{facilityId}/summary", s.withAuth(s.handleSummary))
	mux.HandleFunc("GET /v1/administration/facilities/{facilityId}/profile", s.withAuth(s.handleGetProfile))
	mux.HandleFunc("PUT /v1/administration/facilities/{facilityId}/profile", s.withAuth(s.handlePutProfile))

	mux.HandleFunc("GET /v1/administration/facilities/{facilityId}/sports", s.withAuth(s.handleListSports))
	mux.HandleFunc("POST /v1/administration/facilities/{facilityId}/sports", s.withAuth(s.handleCreateSport))
	mux.HandleFunc("POST /v1/administration/facilities/{facilityId}/sports/{sportId}/status", s.withAuth(s.handleSportStatus))

	mux.HandleFunc("GET /v1/administration/facilities/{facilityId}/courts", s.withAuth(s.handleListCourts))
	mux.HandleFunc("POST /v1/administration/facilities/{facilityId}/courts", s.withAuth(s.handleCreateCourt))
	mux.HandleFunc("POST /v1/administration/facilities/{facilityId}/courts/{courtId}/status", s.withAuth(s.handleCourtStatus))
	mux.HandleFunc("POST /v1/administration/facilities/{facilityId}/sync-sports", s.withAuth(s.handleSyncSports))

	mux.HandleFunc("GET /v1/administration/facilities/{facilityId}/payment-settings", s.withAuth(s.handleListPayments))
	mux.HandleFunc("PUT /v1/administration/facilities/{facilityId}/payment-settings", s.withAuth(s.handlePutPayment))

	mux.HandleFunc("GET /v1/administration/facilities/{facilityId}/services", s.withAuth(s.handleGetServices))
	mux.HandleFunc("PUT /v1/administration/facilities/{facilityId}/services", s.withAuth(s.handlePutServices))

	mux.HandleFunc("GET /v1/administration/facilities/{facilityId}/staff", s.withAuth(s.handleListStaff))
	mux.HandleFunc("POST /v1/administration/facilities/{facilityId}/staff", s.withAuth(s.handleUpsertStaff))
	mux.HandleFunc("POST /v1/administration/facilities/{facilityId}/staff/{membershipId}/status", s.withAuth(s.handleStaffStatus))

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
	if err := s.Admin.DB.Ping(); err != nil {
		ok = false
	}
	status := http.StatusOK
	st := "ok"
	if !ok {
		status = http.StatusServiceUnavailable
		st = "degraded"
	}
	writeJSON(w, status, map[string]interface{}{
		"service":         "administration",
		"status":          st,
		"postgres":        ok,
		"service_key_set": s.Cfg.ServiceKey != "",
		"jwt_ready":       s.JWT != nil,
		"runtime":         "go",
	})
}

func (s *Server) handlePages(w http.ResponseWriter, r *http.Request, _ *authCtx) {
	roles := []string{"admin", "sub_admin", "headcoach", "coach", "tournament_admin"}
	defaults := map[string][]string{}
	for _, role := range roles {
		defaults[role] = administration.RoleDefaults(role)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"pages":         administration.PagesCatalog(),
		"role_defaults": defaults,
	})
}

func (s *Server) facilityAuth(w http.ResponseWriter, r *http.Request, ac *authCtx) (int64, bool) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return 0, false
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return 0, false
	}
	return fid, true
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, ok := s.facilityAuth(w, r, ac)
	if !ok {
		return
	}
	sum, err := s.Admin.Summary(r.Context(), fid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) handleGetProfile(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, ok := s.facilityAuth(w, r, ac)
	if !ok {
		return
	}
	p, err := s.Admin.GetProfile(r.Context(), fid)
	if err != nil {
		code := http.StatusBadRequest
		if err.Error() == "facility_not_found" {
			code = http.StatusNotFound
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handlePutProfile(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, ok := s.facilityAuth(w, r, ac)
	if !ok {
		return
	}
	// Allow logoBase64 payloads (up to ~4MB).
	var raw map[string]interface{}
	if err := json.NewDecoder(io.LimitReader(r.Body, 5<<20)).Decode(&raw); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	in := administration.Profile{}
	b, _ := json.Marshal(raw)
	_ = json.Unmarshal(b, &in)
	// Optional client upload: logoBase64 (+ logoMime) → data URL stored in logo_url.
	if b64, _ := raw["logoBase64"].(string); strings.TrimSpace(b64) != "" {
		mime := "image/jpeg"
		if m, _ := raw["logoMime"].(string); strings.TrimSpace(m) != "" {
			mime = strings.TrimSpace(m)
		}
		in.LogoURL = "data:" + mime + ";base64," + strings.TrimSpace(b64)
	}
	p, err := s.Admin.UpsertProfile(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleListSports(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, ok := s.facilityAuth(w, r, ac)
	if !ok {
		return
	}
	list, err := s.Admin.ListSports(r.Context(), fid, r.URL.Query().Get("status"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"sports": list})
}

func (s *Server) handleCreateSport(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, ok := s.facilityAuth(w, r, ac)
	if !ok {
		return
	}
	var in administration.SportInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Admin.CreateSport(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleSportStatus(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, ok := s.facilityAuth(w, r, ac)
	if !ok {
		return
	}
	sid, err := pathInt(r, "sportId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_sport_id"})
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Admin.UpdateSportStatus(r.Context(), fid, sid, in.Status)
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

func (s *Server) handleSyncSports(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, ok := s.facilityAuth(w, r, ac)
	if !ok {
		return
	}
	var in struct {
		Sports []administration.SportCourtSyncIn `json:"sports"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if err := s.Admin.SyncSportsCourts(r.Context(), fid, in.Sports); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	sports, _ := s.Admin.ListSports(r.Context(), fid, "active")
	courts, _ := s.Admin.ListCourts(r.Context(), fid, "active")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":     true,
		"sports": sports,
		"courts": courts,
		"message": "Sports/courts saved; booking courts synced and slots generated",
	})
}

func (s *Server) handleListCourts(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, ok := s.facilityAuth(w, r, ac)
	if !ok {
		return
	}
	list, err := s.Admin.ListCourts(r.Context(), fid, r.URL.Query().Get("status"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"courts": list})
}

func (s *Server) handleCreateCourt(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, ok := s.facilityAuth(w, r, ac)
	if !ok {
		return
	}
	var in administration.CourtInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Admin.CreateCourt(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleCourtStatus(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, ok := s.facilityAuth(w, r, ac)
	if !ok {
		return
	}
	cid, err := pathInt(r, "courtId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_court_id"})
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Admin.UpdateCourtStatus(r.Context(), fid, cid, in.Status)
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

func (s *Server) handleListPayments(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, ok := s.facilityAuth(w, r, ac)
	if !ok {
		return
	}
	list, err := s.Admin.ListPayments(r.Context(), fid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"payment_settings": list})
}

func (s *Server) handlePutPayment(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, ok := s.facilityAuth(w, r, ac)
	if !ok {
		return
	}
	var in administration.PaymentSetting
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Admin.UpsertPayment(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleGetServices(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, ok := s.facilityAuth(w, r, ac)
	if !ok {
		return
	}
	f, err := s.Admin.GetServices(r.Context(), fid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) handlePutServices(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, ok := s.facilityAuth(w, r, ac)
	if !ok {
		return
	}
	var in administration.ServiceFlags
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	f, err := s.Admin.UpsertServices(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) handleListStaff(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, ok := s.facilityAuth(w, r, ac)
	if !ok {
		return
	}
	list, err := s.Admin.ListStaff(r.Context(), fid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"staff": list})
}

func (s *Server) handleUpsertStaff(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, ok := s.facilityAuth(w, r, ac)
	if !ok {
		return
	}
	var in administration.StaffInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Admin.UpsertStaff(r.Context(), fid, in)
	if err != nil {
		code := http.StatusBadRequest
		if err.Error() == "user_not_found" {
			code = http.StatusNotFound
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleStaffStatus(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, ok := s.facilityAuth(w, r, ac)
	if !ok {
		return
	}
	mid, err := pathInt(r, "membershipId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_membership_id"})
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Admin.UpdateStaffStatus(r.Context(), fid, mid, in.Status)
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

func pathInt(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(r.PathValue(name), 10, 64)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
