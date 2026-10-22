package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/facinect/platform/internal/authn"
	"github.com/facinect/platform/internal/config"
	"github.com/facinect/platform/internal/platform"
)

type Server struct {
	Cfg      config.Config
	Platform *platform.Service
	JWT      *authn.Verifier
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/platform/health", s.handleHealth)

	// Onboarding is a pre-login funnel (WhatsApp OTP in the app). Do not require
	// JWT/service-key — local already allowed via APP_ENV; production was 401.
	mux.HandleFunc("POST /v1/onboarding/status", s.withOptionalAuth(s.handleOnboardingStatus))
	mux.HandleFunc("POST /v1/onboarding/draft/get", s.withOptionalAuth(s.handleDraftGet))
	mux.HandleFunc("POST /v1/onboarding/draft/save", s.withOptionalAuth(s.handleDraftSave))
	mux.HandleFunc("POST /v1/onboarding/submit_facility", s.withOptionalAuth(s.handleSubmitFacility))
	mux.HandleFunc("POST /v1/onboarding/submit_tournament", s.withOptionalAuth(s.handleSubmitTournament))
	mux.HandleFunc("POST /v1/onboarding/submit_referee", s.withOptionalAuth(s.handleSubmitReferee))
	mux.HandleFunc("GET /v1/onboarding/sports", s.withOptionalAuth(s.handleSports))
	mux.HandleFunc("GET /v1/onboarding/manage", s.handleOnboardingManage)
	mux.HandleFunc("POST /v1/onboarding/manage/approve", s.handleOnboardingManageApprove)
	mux.HandleFunc("GET /v1/onboarding/whatsapp/approve/{token}", s.handleOnboardingApproveLink)
	mux.HandleFunc("GET /v1/onboarding/whatsapp/view/{token}", s.handleOnboardingViewLink)

	mux.HandleFunc("GET /v1/news", s.withAuth(s.handleNewsList))
	mux.HandleFunc("POST /v1/news/save", s.withAuth(s.handleNewsSave))
	mux.HandleFunc("GET /v1/news/inbox", s.withAuth(s.handleNewsInbox))

	mux.HandleFunc("GET /v1/organisation/profile", s.withAuth(s.handleOrgGet))
	mux.HandleFunc("POST /v1/organisation/profile", s.withAuth(s.handleOrgSave))

	mux.HandleFunc("GET /v1/controls/sync", s.withAuth(s.handleControlsSync))

	// Privacy accept during onboarding may run before a stored JWT exists.
	mux.HandleFunc("POST /v1/privacy", s.withOptionalAuth(s.handlePrivacy))

	mux.HandleFunc("GET /v1/billing/entitlement", s.withAuth(s.handleBillingEntitlement))
	mux.HandleFunc("GET /v1/billing/catalog", s.withAuth(s.handleBillingCatalog))
	mux.HandleFunc("POST /v1/billing/claim_trial", s.withAuth(s.handleClaimTrial))
	mux.HandleFunc("POST /v1/billing/checkout", s.withAuth(s.handleCheckout))
	mux.HandleFunc("POST /v1/billing/confirm", s.withAuth(s.handleConfirm))

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
	Email     string
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

// withOptionalAuth parses Bearer/service-key when present, but never 401s.
// Used for public onboarding (draft/submit/sports) before facility login.
func (s *Server) withOptionalAuth(next func(http.ResponseWriter, *http.Request, *authCtx)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ac, err := s.authorize(r)
		if err != nil {
			ac = &authCtx{}
		}
		next(w, r, ac)
	}
}

// Local auth: allow if APP_ENV=local OR Bearer present OR service key.
func (s *Server) authorize(r *http.Request) (*authCtx, error) {
	key := strings.TrimSpace(r.Header.Get("X-Service-Key"))
	if s.Cfg.ServiceKey != "" && key != "" && key == s.Cfg.ServiceKey {
		return &authCtx{ViaKey: true}, nil
	}
	if s.Cfg.ServiceKey == "" && key != "" {
		return &authCtx{ViaKey: true}, nil
	}
	authz := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(authz), "bearer ") {
		token := strings.TrimSpace(authz[7:])
		if token != "" {
			ac := &authCtx{}
			if s.JWT != nil {
				if p, err := s.JWT.ParseAccess(token); err == nil {
					ac.Principal = p
					ac.Email = p.Email
				}
			}
			return ac, nil
		}
	}
	if s.Cfg.IsLocal() {
		return &authCtx{ViaKey: true}, nil
	}
	return nil, errUnauthorized
}

var errUnauthorized = &authError{"unauthorized"}

type authError struct{ s string }

func (e *authError) Error() string { return e.s }

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ok := true
	if err := s.Platform.DB.Ping(); err != nil {
		ok = false
	}
	status := http.StatusOK
	st := "ok"
	if !ok {
		status = http.StatusServiceUnavailable
		st = "degraded"
	}
	writeJSON(w, status, map[string]interface{}{
		"service":         "platform",
		"status":          st,
		"postgres":        ok,
		"service_key_set": s.Cfg.ServiceKey != "",
		"jwt_ready":       s.JWT != nil,
		"runtime":         "go",
	})
}

func (s *Server) handleOnboardingStatus(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	var in platform.OnboardingStatusIn
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	writeJSON(w, http.StatusOK, s.Platform.OnboardingStatus(r.Context(), in))
}

func (s *Server) handleDraftGet(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	var in platform.DraftIn
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	email := in.Email
	if email == "" {
		email = ac.Email
	}
	out, err := s.Platform.GetDraft(r.Context(), email)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDraftSave(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	var in platform.DraftIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if in.Email == "" {
		in.Email = ac.Email
	}
	out, err := s.Platform.SaveDraft(r.Context(), in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) submitKind(w http.ResponseWriter, r *http.Request, ac *authCtx, kind string) {
	var in platform.SubmitIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		// allow empty body
		in = platform.SubmitIn{}
	}
	if in.Email == "" {
		in.Email = ac.Email
	}
	// store full body as payload if payload empty
	if len(in.Payload) == 0 {
		raw, _ := json.Marshal(in)
		in.Payload = raw
	}
	out, err := s.Platform.SubmitOnboarding(r.Context(), kind, in)
	if err != nil {
		if errors.Is(err, platform.ErrDuplicateOnboarding) {
			if out == nil {
				out = map[string]interface{}{
					"success": false,
					"error":   "duplicate_onboarding",
					"message": "Already submitted — wait for approval.",
				}
			}
			writeJSON(w, http.StatusConflict, out)
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSubmitFacility(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	s.submitKind(w, r, ac, "facility")
}

func (s *Server) handleSubmitTournament(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	s.submitKind(w, r, ac, "tournament")
}

func (s *Server) handleSubmitReferee(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	s.submitKind(w, r, ac, "referee")
}

func (s *Server) handleSports(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	writeJSON(w, http.StatusOK, s.Platform.SportsCatalog())
}

func (s *Server) handleOnboardingManage(w http.ResponseWriter, r *http.Request) {
	focus, _ := strconv.ParseInt(r.URL.Query().Get("focus"), 10, 64)
	page, err := s.Platform.OnboardingManagePage(r.Context(), focus, r.URL.Query().Get("notice"))
	if err != nil {
		http.Error(w, "Could not load onboarding requests.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(page))
}

func (s *Server) handleOnboardingManageApprove(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/v1/onboarding/manage?notice=Invalid+request", http.StatusSeeOther)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	notice := "Approved"
	if err := s.Platform.ApproveOnboardingID(r.Context(), id); err != nil {
		notice = err.Error()
	}
	http.Redirect(w, r, fmt.Sprintf("/v1/onboarding/manage?focus=%d&notice=%s", id, url.QueryEscape(notice)), http.StatusSeeOther)
}

func (s *Server) handleOnboardingApproveLink(w http.ResponseWriter, r *http.Request) {
	page, status := s.Platform.ApproveOnboardingLink(r.Context(), r.PathValue("token"))
	if page == "" && status < 400 {
		http.Redirect(w, r, "/v1/onboarding/manage?notice=Approved", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(page))
}

func (s *Server) handleOnboardingViewLink(w http.ResponseWriter, r *http.Request) {
	target, err := s.Platform.ViewOnboardingLink(r.PathValue("token"))
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<!doctype html><html><body><p>This view link is not valid.</p></body></html>`))
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) handleNewsList(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	audience := r.URL.Query().Get("audience")
	fid, _ := strconv.ParseInt(r.URL.Query().Get("facilityId"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ids := []int64{}
	if raw := strings.TrimSpace(r.URL.Query().Get("facilityIds")); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			n, _ := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
			if n > 0 {
				ids = append(ids, n)
			}
		}
	}
	out, err := s.Platform.ListNews(r.Context(), audience, fid, ids, limit)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleNewsSave(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	var body struct {
		NewsID int64 `json:"newsId"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	userKey := ac.Email
	if userKey == "" && ac.Principal != nil {
		userKey = strconv.FormatInt(ac.Principal.UserID, 10)
	}
	out, err := s.Platform.SaveNews(r.Context(), body.NewsID, userKey)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleNewsInbox(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	userKey := ac.Email
	if userKey == "" && ac.Principal != nil {
		userKey = strconv.FormatInt(ac.Principal.UserID, 10)
	}
	out, err := s.Platform.NewsInbox(r.Context(), userKey)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleOrgGet(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	email := r.URL.Query().Get("email")
	if email == "" {
		email = ac.Email
	}
	out, err := s.Platform.GetOrgProfile(r.Context(), email)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleOrgSave(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	var in platform.OrgProfileIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if in.Email == "" {
		in.Email = ac.Email
	}
	out, err := s.Platform.SaveOrgProfile(r.Context(), in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleControlsSync(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, _ := strconv.ParseInt(r.URL.Query().Get("facilityId"), 10, 64)
	since := r.URL.Query().Get("since")
	timeout, _ := strconv.Atoi(r.URL.Query().Get("timeout"))
	out, err := s.Platform.ControlsSync(r.Context(), fid, since, timeout)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePrivacy(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	var in platform.PrivacyIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if in.Email == "" {
		in.Email = ac.Email
	}
	out, err := s.Platform.Privacy(r.Context(), in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleBillingEntitlement(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, _ := strconv.ParseInt(r.URL.Query().Get("facilityId"), 10, 64)
	writeJSON(w, http.StatusOK, s.Platform.BillingEntitlement(fid))
}

func (s *Server) handleBillingCatalog(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	writeJSON(w, http.StatusOK, s.Platform.BillingCatalog())
}

func (s *Server) handleClaimTrial(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	var body struct {
		FacilityID int64 `json:"facilityId"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	writeJSON(w, http.StatusOK, s.Platform.ClaimTrial(body.FacilityID))
}

func (s *Server) handleCheckout(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	var in platform.CheckoutIn
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	writeJSON(w, http.StatusOK, s.Platform.Checkout(r.Context(), in))
}

func (s *Server) handleConfirm(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	var in platform.ConfirmIn
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	writeJSON(w, http.StatusOK, s.Platform.ConfirmBilling(r.Context(), in))
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
