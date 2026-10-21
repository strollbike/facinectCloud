package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/facinect/identity/internal/auth"
	"github.com/facinect/identity/internal/config"
)

type Server struct {
	Cfg  config.Config
	Auth *auth.Service
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/auth/health", s.handleHealth)
	mux.HandleFunc("GET /v1/auth/openapi.json", s.handleOpenAPI)
	mux.HandleFunc("POST /v1/auth/check-email", s.handleCheckEmail)
	mux.HandleFunc("POST /v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /v1/auth/google", s.handleGoogleIDToken)
	mux.HandleFunc("GET /v1/auth/google/start", s.handleGoogleStart)
	mux.HandleFunc("GET /v1/auth/google/callback", s.handleGoogleCallback)
	mux.HandleFunc("POST /v1/auth/google/handoff", s.handleGoogleHandoff)
	mux.HandleFunc("POST /v1/auth/otp/request", s.handleOTPRequest)
	mux.HandleFunc("POST /v1/auth/otp/verify", s.handleOTPVerify)
	mux.HandleFunc("POST /v1/auth/token/refresh", s.handleRefresh)
	mux.HandleFunc("POST /v1/auth/logout", s.handleLogout)
	mux.HandleFunc("POST /v1/auth/password", s.handleSetPassword)
	mux.HandleFunc("GET /v1/auth/me", s.handleMe)
	mux.HandleFunc("GET /v1/auth/session", s.handleSession)
	mux.HandleFunc("POST /v1/auth/session/refresh", s.handleSession)
	mux.HandleFunc("GET /v1/auth/jwks.json", s.handleJWKS)
	mux.HandleFunc("GET /v1/auth/ui", s.redirectLogin)
	mux.HandleFunc("GET /v1/auth/ui/", s.redirectLogin)
	return s.withCORS(mux)
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := s.corsOrigin(); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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

func (s *Server) redirectLogin(w http.ResponseWriter, r *http.Request) {
	q := r.URL.RawQuery
	loc := "/login"
	if q != "" {
		loc += "?" + q
	}
	http.Redirect(w, r, loc, http.StatusFound)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ok := true
	if err := s.Auth.DB.Ping(); err != nil {
		ok = false
	}
	status := http.StatusOK
	st := "ok"
	if !ok {
		status = http.StatusServiceUnavailable
		st = "degraded"
	}
	writeJSON(w, status, map[string]interface{}{
		"service":      "identity",
		"status":       st,
		"postgres":     ok,
		"google_oauth": s.Cfg.GoogleConfigured(),
		"runtime":      "go",
	})
}

func (s *Server) handleCheckEmail(w http.ResponseWriter, r *http.Request) {
	body := readJSON(r)
	email, _ := body["email"].(string)
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_email"})
		return
	}
	exists := s.Auth.EmailExists(email)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"exists":       exists,
		"can_password": exists,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	body := readJSON(r)
	email, _ := body["email"].(string)
	password, _ := body["password"].(string)
	pair, err := s.Auth.LoginWithPassword(email, password, clientIP(r))
	if err != nil {
		status := http.StatusUnauthorized
		if err == auth.ErrNoFacilitiesAssigned {
			status = http.StatusForbidden
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, pair)
}

// handleGoogleIDToken — native Google Sign-In (StrollX / Flutter) id_token → JWT.
func (s *Server) handleGoogleIDToken(w http.ResponseWriter, r *http.Request) {
	body := readJSON(r)
	idToken, _ := body["id_token"].(string)
	if strings.TrimSpace(idToken) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id_token_required"})
		return
	}
	if s.Auth.Google == nil || !s.Auth.Google.HasClientID() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "oauth_not_configured"})
		return
	}
	info, err := s.Auth.Google.VerifyIDToken(idToken)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	pair, err := s.Auth.LoginWithGoogle(info, clientIP(r))
	if err != nil {
		status := http.StatusUnauthorized
		switch err {
		case auth.ErrNoFacilitiesAssigned:
			status = http.StatusForbidden
		case auth.ErrEmailNotVerified:
			status = http.StatusForbidden
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, pair)
}

func (s *Server) handleGoogleStart(w http.ResponseWriter, r *http.Request) {
	if !s.Auth.Google.Configured() {
		http.Redirect(w, r, "/login?error=oauth_config", http.StatusFound)
		return
	}
	state, err := auth.GenerateState()
	if err != nil {
		http.Redirect(w, r, "/login?error=oauth_failed", http.StatusFound)
		return
	}
	verifier, err := auth.GenerateCodeVerifier()
	if err != nil {
		http.Redirect(w, r, "/login?error=oauth_failed", http.StatusFound)
		return
	}
	challenge := auth.CodeChallenge(verifier)
	if err := s.Auth.SavePKCE(state, verifier, s.Auth.Google.RedirectURI); err != nil {
		http.Redirect(w, r, "/login?error=oauth_failed", http.StatusFound)
		return
	}
	http.Redirect(w, r, s.Auth.Google.BuildAuthorizationURL(state, challenge), http.StatusFound)
}

func (s *Server) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if errParam := q.Get("error"); errParam != "" {
		code := "oauth_failed"
		if errParam == "access_denied" {
			code = "oauth_denied"
		}
		http.Redirect(w, r, "/login?error="+url.QueryEscape(code), http.StatusFound)
		return
	}
	code := strings.TrimSpace(q.Get("code"))
	state := strings.TrimSpace(q.Get("state"))
	if code == "" || state == "" {
		http.Redirect(w, r, "/login?error=invalid_state", http.StatusFound)
		return
	}
	verifier, redirectURI, ok := s.Auth.ConsumePKCE(state)
	if !ok {
		http.Redirect(w, r, "/login?error=oauth_expired", http.StatusFound)
		return
	}
	if redirectURI != s.Auth.Google.RedirectURI {
		http.Redirect(w, r, "/login?error=oauth_redirect_mismatch", http.StatusFound)
		return
	}
	access, err := s.Auth.Google.ExchangeCode(code, verifier)
	if err != nil {
		msg := err.Error()
		errCode := "oauth_failed"
		if strings.Contains(msg, "invalid_client") {
			errCode = "oauth_bad_secret"
		} else if strings.Contains(msg, "redirect_uri_mismatch") {
			errCode = "oauth_redirect_mismatch"
		}
		http.Redirect(w, r, "/login?error="+url.QueryEscape(errCode), http.StatusFound)
		return
	}
	info, err := s.Auth.Google.FetchUserInfo(access)
	if err != nil {
		http.Redirect(w, r, "/login?error=oauth_failed", http.StatusFound)
		return
	}
	pair, err := s.Auth.LoginWithGoogle(info, clientIP(r))
	if err != nil {
		http.Redirect(w, r, "/login?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	handoff, err := s.Auth.CreateHandoff(pair)
	if err != nil {
		http.Redirect(w, r, "/login?error=oauth_failed", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/login?google=1&handoff="+url.QueryEscape(handoff), http.StatusFound)
}

func (s *Server) handleGoogleHandoff(w http.ResponseWriter, r *http.Request) {
	body := readJSON(r)
	handoff, _ := body["handoff"].(string)
	pair, err := s.Auth.ConsumeHandoff(strings.TrimSpace(handoff))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, pair)
}

func (s *Server) handleOTPRequest(w http.ResponseWriter, r *http.Request) {
	body := readJSON(r)
	phone, _ := body["phone"].(string)
	if phone == "" {
		phone, _ = body["whatsappNo"].(string)
	}
	out, err := s.Auth.RequestOTP(phone)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleOTPVerify(w http.ResponseWriter, r *http.Request) {
	body := readJSON(r)
	phone, _ := body["phone"].(string)
	if phone == "" {
		phone, _ = body["whatsappNo"].(string)
	}
	code, _ := body["code"].(string)
	if code == "" {
		code, _ = body["otp"].(string)
	}
	pair, err := s.Auth.VerifyOTP(phone, code, clientIP(r))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, pair)
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	body := readJSON(r)
	refresh, _ := body["refresh_token"].(string)
	pair, err := s.Auth.Refresh(refresh)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, pair)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	body := readJSON(r)
	refresh, _ := body["refresh_token"].(string)
	s.Auth.RevokeRefresh(refresh)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSetPassword(w http.ResponseWriter, r *http.Request) {
	user, _, err := s.userFromBearer(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	body := readJSON(r)
	next, _ := body["new_password"].(string)
	if next == "" {
		next, _ = body["password"].(string)
	}
	current, _ := body["current_password"].(string)
	if err := s.Auth.SetPassword(user.ID, current, next); err != nil {
		status := http.StatusBadRequest
		if err == auth.ErrUnauthorized {
			status = http.StatusUnauthorized
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, claims, err := s.userFromBearer(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	session, _, redirect, err := s.Auth.SessionPayload(user, queryFacilityID(r))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session_failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user":     session,
		"redirect": redirect,
		"claims": map[string]interface{}{
			"sub":        claims["sub"],
			"iss":        claims["iss"],
			"aud":        claims["aud"],
			"exp":        claims["exp"],
			"role":       claims["role"],
			"facilities": claims["facilities"],
		},
	})
}

// handleSession mirrors Facinect api/v2/auth.php refresh_session — live membership from DB.
// Optional ?facilityId= selects that facility's role/page_access for multi-facility staff.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	user, _, err := s.userFromBearer(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	session, memberships, redirect, err := s.Auth.SessionPayload(user, queryFacilityID(r))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session_failed"})
		return
	}
	if len(memberships) == 0 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no_facilities_assigned"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":       true,
		"user":     session,
		"redirect": redirect,
	})
}

func queryFacilityID(r *http.Request) int64 {
	raw := strings.TrimSpace(r.URL.Query().Get("facilityId"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("facility_id"))
	}
	if raw == "" {
		return 0
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

func (s *Server) userFromBearer(r *http.Request) (*auth.User, map[string]interface{}, error) {
	authz := r.Header.Get("Authorization")
	if !strings.HasPrefix(strings.ToLower(authz), "bearer ") {
		return nil, nil, fmt.Errorf("missing_bearer")
	}
	token := strings.TrimSpace(authz[7:])
	claims, err := s.Auth.JWT.ParseAccess(token)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid_token")
	}
	sub, _ := claims["sub"].(string)
	uid, _ := strconv.ParseInt(sub, 10, 64)
	user, err := s.Auth.UserByID(uid)
	if err != nil {
		return nil, nil, fmt.Errorf("user_not_found")
	}
	if user.Status != "active" {
		return nil, nil, fmt.Errorf("unauthorized")
	}
	return user, claims, nil
}

func (s *Server) handleJWKS(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Auth.JWT.JWKS())
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request) map[string]interface{} {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(body) == 0 {
		return map[string]interface{}{}
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		return map[string]interface{}{}
	}
	return out
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[:i]
	}
	return host
}
