package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/facinect/paygateway/internal/authn"
	"github.com/facinect/paygateway/internal/config"
	"github.com/facinect/paygateway/internal/gateway"
)

type Server struct {
	Cfg     config.Config
	Gateway *gateway.Service
	JWT     *authn.Verifier
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/gateway/health", s.handleHealth)
	mux.HandleFunc("GET /v1/gateway/openapi.json", s.handleOpenAPI)

	mux.HandleFunc("GET /v1/gateway/providers", s.withAuth(s.handleProviders))
	mux.HandleFunc("POST /v1/gateway/orders", s.withAuth(s.handleCreateOrder))
	mux.HandleFunc("POST /v1/gateway/payment-links", s.withAuth(s.handleCreatePaymentLink))
	mux.HandleFunc("POST /v1/gateway/payment-links/{linkId}/cancel", s.withAuth(s.handleCancelPaymentLink))
	mux.HandleFunc("POST /v1/gateway/verify", s.withAuth(s.handleVerify))

	// Webhooks: provider signature, no JWT.
	// subscription = facility plan. razorpay = booking, coaching, membership, tournament, fees.
	// Both use Razorpay until SUBSCRIPTION_PROVIDER is changed.
	mux.HandleFunc("GET /v1/gateway/webhooks/subscription", s.handleWebhookReady("subscription"))
	mux.HandleFunc("POST /v1/gateway/webhooks/subscription", s.handleChannelWebhook("subscription"))
	mux.HandleFunc("GET /v1/gateway/webhooks/razorpay", s.handleWebhookReady("razorpay"))
	mux.HandleFunc("POST /v1/gateway/webhooks/razorpay", s.handleChannelWebhook("razorpay"))
	mux.HandleFunc("POST /v1/gateway/webhooks/{provider}", s.handleWebhook)

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
	return nil, errUnauthorized
}

var errUnauthorized = &authError{"unauthorized"}

type authError struct{ s string }

func (e *authError) Error() string { return e.s }

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ok := true
	if err := s.Gateway.DB.Ping(); err != nil {
		ok = false
	}
	status := http.StatusOK
	st := "ok"
	if !ok {
		status = http.StatusServiceUnavailable
		st = "degraded"
	}
	providers := s.Gateway.Providers()
	writeJSON(w, status, map[string]interface{}{
		"service":         "paygateway",
		"status":          st,
		"postgres":        ok,
		"service_key_set": s.Cfg.ServiceKey != "",
		"jwt_ready":       s.JWT != nil,
		"runtime":         "go",
		"providers":       providers,
	})
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request, _ *authCtx) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"providers": s.Gateway.Providers(),
		"default":   s.Cfg.DefaultProvider,
	})
}

func (s *Server) handleCreateOrder(w http.ResponseWriter, r *http.Request, _ *authCtx) {
	var in gateway.CreateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	res, err := s.Gateway.Create(r.Context(), in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleCreatePaymentLink(w http.ResponseWriter, r *http.Request, _ *authCtx) {
	var in gateway.PaymentLinkRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	res, err := s.Gateway.CreatePaymentLink(r.Context(), in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}


func (s *Server) handleCancelPaymentLink(w http.ResponseWriter, r *http.Request, _ *authCtx) {
	linkID := strings.TrimSpace(r.PathValue("linkId"))
	if linkID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_link_id"})
		return
	}
	if err := s.Gateway.CancelPaymentLink(r.Context(), linkID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "link_id": linkID, "status": "cancelled"})
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request, _ *authCtx) {
	var in gateway.VerifyRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	res, err := s.Gateway.Verify(r.Context(), in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleWebhookReady(channel string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"ok":      "true",
			"channel": channel,
			"message": "Razorpay must POST the event JSON with X-Razorpay-Signature.",
		})
	}
}

func (s *Server) handleChannelWebhook(channel string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_body"})
			return
		}
		ev, err := s.Gateway.HandleChannelWebhook(r.Context(), channel, "", r.Header, body)
		if err != nil {
			code := http.StatusBadRequest
			if strings.Contains(err.Error(), "signature") {
				code = http.StatusUnauthorized
			}
			writeJSON(w, code, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, ev)
	}
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	provider := strings.ToLower(strings.TrimSpace(r.PathValue("provider")))
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_body"})
		return
	}
	ev, err := s.Gateway.HandleWebhook(r.Context(), provider, r.Header, body)
	if err != nil {
		code := http.StatusBadRequest
		if strings.Contains(err.Error(), "signature") {
			code = http.StatusUnauthorized
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
