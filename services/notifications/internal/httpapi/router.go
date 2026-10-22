package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/facinect/notifications/internal/authn"
	"github.com/facinect/notifications/internal/config"
	"github.com/facinect/notifications/internal/notify"
)

type Server struct {
	Cfg        config.Config
	Notify     *notify.Service
	JWT        *authn.Verifier
	lookupRole func(ctx context.Context, userID, facilityID int64) (string, error)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/notifications/health", s.handleHealth)
	mux.HandleFunc("GET /v1/notifications/openapi.json", s.handleOpenAPI)
	mux.HandleFunc("POST /v1/notifications/send", s.withAuth(s.handleSend))
	mux.HandleFunc("POST /v1/notifications/send-bulk", s.withSendBulkAuth(s.handleSendBulk))
	mux.HandleFunc("POST /v1/notifications/service-broadcast", s.withAuth(s.handleServiceBroadcast))
	mux.HandleFunc("GET /v1/notifications/service-broadcasts", s.withAuth(s.handleListServiceBroadcasts))
	mux.HandleFunc("GET /v1/notifications/facilities/{facilityId}/whatsapp", s.withAuth(s.handleGetFacilityWA))
	mux.HandleFunc("PUT /v1/notifications/facilities/{facilityId}/whatsapp", s.withAuth(s.handlePutFacilityWA))
	mux.HandleFunc("GET /v1/notifications/whatsapp/lookup", s.withAuth(s.handleLookupByPhone))
	mux.HandleFunc("GET /v1/notifications/history", s.withAuth(s.handleHistory))
	mux.HandleFunc("GET /v1/notifications/inbox", s.withAuth(s.handleInbox))
	mux.HandleFunc("GET /v1/notifications/{id}", s.withAuth(s.handleGetJob))
	mux.HandleFunc("POST /v1/notifications/devices", s.withAuth(s.handleRegisterDevice))
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

// corsOrigin: production never emits wildcard *; local may use *.
func (s *Server) corsOrigin() string {
	o := strings.TrimSpace(s.Cfg.CORSOrigins)
	if o == "" {
		if s.Cfg.AppEnv == "local" || s.Cfg.AppEnv == "development" {
			return "*"
		}
		return ""
	}
	if o == "*" && s.Cfg.AppEnv != "local" && s.Cfg.AppEnv != "development" {
		return ""
	}
	return o
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

// authorized: X-Service-Key must match when configured.
// Production requires a non-empty NOTIFICATIONS_SERVICE_KEY (no unverified Bearer).
// Local/dev may omit the key for convenience.
func (s *Server) authorized(r *http.Request) bool {
	key := strings.TrimSpace(r.Header.Get("X-Service-Key"))
	if s.Cfg.ServiceKey != "" {
		return key != "" && key == s.Cfg.ServiceKey
	}
	if s.Cfg.AppEnv == "local" || s.Cfg.AppEnv == "development" {
		return true
	}
	return false
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ok := true
	if err := s.Notify.DB.Ping(); err != nil {
		ok = false
	}
	status := http.StatusOK
	st := "ok"
	if !ok {
		status = http.StatusServiceUnavailable
		st = "degraded"
	}
	writeJSON(w, status, map[string]interface{}{
		"service":             "notifications",
		"status":              st,
		"postgres":            ok,
		"whatsapp":            s.Cfg.WhatsAppConfigured(),
		"push":                s.Cfg.PushConfigured(),
		"dry_run":             s.Cfg.DryRun,
		"service_key_set":     s.Cfg.ServiceKey != "",
		"runtime":             "go",
	})
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req notify.SendRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	job, err := s.Notify.Send(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Surface provider failures so clients can fall back (e.g. template → text).
	if job != nil && strings.EqualFold(job.Status, "failed") {
		errMsg := strings.TrimSpace(job.ErrorText)
		if errMsg == "" {
			errMsg = "send_failed"
		}
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{
			"error":      errMsg,
			"error_text": job.ErrorText,
			"status":     job.Status,
			"id":         job.ID,
			"job":        job,
		})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleServiceBroadcast(w http.ResponseWriter, r *http.Request) {
	var req notify.ServiceBroadcastRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	result, err := s.Notify.ServiceBroadcast(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	status := http.StatusOK
	if result != nil && !result.OK {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, result)
}

func (s *Server) handleListServiceBroadcasts(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.Notify.ListServiceBroadcasts(r.Context(), limit)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}

func (s *Server) handleSendBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Channel     string                   `json:"channel"`
		FacilityID  *int64                   `json:"facility_id"`
		TemplateKey string                   `json:"template"`
		Recipients  []notify.SendTo          `json:"recipients"`
		Data        map[string]interface{}   `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if len(body.Recipients) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "recipients_required"})
		return
	}
	if len(body.Recipients) > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "recipients_limit_200"})
		return
	}
	tmpl := strings.TrimSpace(body.TemplateKey)
	if tmpl == "" {
		tmpl = "hello_world1"
	}
	data := body.Data
	if data == nil {
		data = map[string]interface{}{}
	}
	if tmpl == "hello_world1" {
		if _, ok := data["header_image"]; !ok {
			data["header_image"] = "https://strollbike.com/Services/Facinect/images/poster.jpeg"
		}
		if _, ok := data["quick_reply_payload"]; !ok {
			data["quick_reply_payload"] = "I_AM_INTERESTED"
		}
	}
	jobs := make([]*notify.Job, 0, len(body.Recipients))
	for _, to := range body.Recipients {
		job, err := s.Notify.Send(r.Context(), notify.SendRequest{
			Channel:     body.Channel,
			FacilityID:  body.FacilityID,
			TemplateKey: tmpl,
			To:          to,
			Data:        data,
		})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"error": err.Error(),
				"jobs":  jobs,
			})
			return
		}
		jobs = append(jobs, job)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count": len(jobs),
		"jobs":  jobs,
	})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_id"})
		return
	}
	job, err := s.Notify.GetJob(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleRegisterDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID   int64  `json:"user_id"`
		Token    string `json:"token"`
		Platform string `json:"platform"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if err := s.Notify.RegisterDevice(r.Context(), body.UserID, body.Token, body.Platform); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleGetFacilityWA(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("facilityId"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	cfg, err := s.Notify.GetFacilityWhatsApp(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handlePutFacilityWA(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("facilityId"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	var body notify.FacilityWhatsAppUpsert
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	cfg, err := s.Notify.UpsertFacilityWhatsApp(r.Context(), id, body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handleLookupByPhone(w http.ResponseWriter, r *http.Request) {
	phoneID := strings.TrimSpace(r.URL.Query().Get("phone_number_id"))
	if phoneID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "phone_number_id_required"})
		return
	}
	fid, err := s.Notify.FindFacilityByPhoneNumberID(r.Context(), phoneID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"facility_id": fid})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var facilityID *int64
	fidStr := strings.TrimSpace(r.URL.Query().Get("facilityId"))
	if fidStr == "" {
		fidStr = strings.TrimSpace(r.URL.Query().Get("facility_id"))
	}
	if fidStr != "" {
		n, err := strconv.ParseInt(fidStr, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
			return
		}
		facilityID = &n
	}
	items, err := s.Notify.History(r.Context(), facilityID, limit)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var userID *int64
	uidStr := strings.TrimSpace(r.URL.Query().Get("userId"))
	if uidStr == "" {
		uidStr = strings.TrimSpace(r.URL.Query().Get("user_id"))
	}
	if uidStr != "" {
		n, err := strconv.ParseInt(uidStr, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_user_id"})
			return
		}
		userID = &n
	}
	email := strings.TrimSpace(r.URL.Query().Get("email"))
	items, err := s.Notify.Inbox(r.Context(), userID, email, limit)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
