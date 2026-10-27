package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/facinect/tournaments/internal/authn"
	"github.com/facinect/tournaments/internal/config"
	"github.com/facinect/tournaments/internal/tournaments"
)

type Server struct {
	Cfg         config.Config
	Tournaments *tournaments.Service
	JWT         *authn.Verifier
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/tournaments/health", s.handleHealth)
	mux.HandleFunc("GET /v1/tournaments/openapi.json", s.handleOpenAPI)

	mux.HandleFunc("GET /v1/tournaments/facilities/{facilityId}/tournaments/summary", s.withAuth(s.handleSummary))
	mux.HandleFunc("GET /v1/tournaments/facilities/{facilityId}/tournaments", s.withAuth(s.handleList))
	mux.HandleFunc("POST /v1/tournaments/facilities/{facilityId}/tournaments", s.withAuth(s.handleCreate))

	mux.HandleFunc("GET /v1/tournaments/tournaments/{tournamentId}", s.withAuth(s.handleGet))
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}", s.withAuth(s.handleUpdate))
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/status", s.withAuth(s.handleStatus))

	// Phase-2
	mux.HandleFunc("GET /v1/tournaments/tournaments/{tournamentId}/entries", s.withAuth(s.handleListEntries))
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/entries", s.withAuth(s.handleCreateEntry))
	mux.HandleFunc("POST /v1/tournaments/entries/{entryId}/payment", s.withAuth(s.handleEntryPayment))

	mux.HandleFunc("GET /v1/tournaments/tournaments/{tournamentId}/fixtures", s.withAuth(s.handleListFixtures))
	mux.HandleFunc("GET /v1/tournaments/tournaments/{tournamentId}/standings", s.withAuth(s.handleStandings))
	mux.HandleFunc("GET /v1/tournaments/tournaments/{tournamentId}/courts-board", s.withAuth(s.handleCourtsBoard))
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/courts-board", s.withAuth(s.handleCourtsBoardPost))
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/fixtures", s.withAuth(s.handleGenerateFixtures))
	mux.HandleFunc("POST /v1/tournaments/fixtures/{fixtureId}/score", s.withAuth(s.handleFixtureScore))

	mux.HandleFunc("POST /v1/tournaments/entries/{entryId}/checkout", s.withAuth(s.handleEntryCheckout))
	mux.HandleFunc("POST /v1/tournaments/entries/{entryId}/confirm-payment", s.withAuth(s.handleEntryConfirmPayment))
	mux.HandleFunc("POST /v1/tournaments/payments/gateway-paid", s.withAuth(s.handleGatewayPaid))

	mux.HandleFunc("GET /v1/tournaments/tournaments/{tournamentId}/score-live", s.withAuth(s.handleScoreLive))

	mux.HandleFunc("GET /v1/tournaments/tournaments/{tournamentId}/template", s.withAuth(s.handleGetTemplate))
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/template", s.withAuth(s.handleSaveTemplate))
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/template/delete", s.withAuth(s.handleDeleteTemplate))

	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/publish", s.withAuth(s.handlePublish))

	mux.HandleFunc("GET /v1/tournaments/referees", s.withAuth(s.handleListRefereesFacility))
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/referees/assign-court", s.withAuth(s.handleAssignCourt))
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/referees/unassign-court", s.withAuth(s.handleUnassignCourt))
	mux.HandleFunc("GET /v1/tournaments/tournaments/{tournamentId}/referees/court", s.withAuth(s.handleRefereesCourt))

	// Advanced
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/fixtures/knockout", s.withAuth(s.handleGenerateKnockout))
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/fixtures/next-round", s.withAuth(s.handleGenerateNextRound))
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/fixtures/preview", s.withAuth(s.handleFixturePreview))
	mux.HandleFunc("POST /v1/tournaments/fixtures/{fixtureId}/walkover", s.withAuth(s.handleWalkover))
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/fixtures/reset", s.withAuth(s.handleResetFixtures))
	mux.HandleFunc("GET /v1/tournaments/tournaments/{tournamentId}/players-template", s.withAuth(s.handlePlayersTemplate))
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/players/import", s.withAuth(s.handlePlayersImport))
	mux.HandleFunc("GET /v1/tournaments/tournaments/{tournamentId}/ledger", s.withAuth(s.handleLedger))
	mux.HandleFunc("GET /v1/tournaments/tournaments/{tournamentId}/form-fields", s.withAuth(s.handleGetFormFields))
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/form-fields", s.withAuth(s.handleSaveFormFields))
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/notify-contacts", s.withAuth(s.handleNotifyContacts))
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/entries/mark-all-paid", s.withAuth(s.handleMarkAllPaid))
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/referees/assign-match", s.withAuth(s.handleAssignMatch))
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/referees/unassign-match", s.withAuth(s.handleUnassignMatch))
	mux.HandleFunc("GET /v1/tournaments/referees/owner-history", s.withAuth(s.handleOwnerHistory))
	mux.HandleFunc("POST /v1/tournaments/referees/validate", s.withAuth(s.handleValidateReferee))
	mux.HandleFunc("POST /v1/tournaments/tournaments/{tournamentId}/ref-link", s.withAuth(s.handleCreateRefLink))

	// Public token referee (no JWT)
	mux.HandleFunc("GET /v1/tournaments/ref/matches", s.handleRefMatches)
	mux.HandleFunc("POST /v1/tournaments/ref/score", s.handleRefScore)
	mux.HandleFunc("POST /v1/tournaments/ref/save", s.handleRefSave)
	mux.HandleFunc("GET /v1/tournaments/ref/history", s.handleRefHistory)
	mux.HandleFunc("POST /v1/tournaments/ref/login", s.handleRefLogin)
	mux.HandleFunc("POST /v1/tournaments/ref/unlock", s.handleRefUnlock)
	mux.HandleFunc("GET /v1/tournaments/ref/validate-assignment", s.handleRefValidateAssignment)

	// Stage G — public live board / score bus (no JWT)
	mux.HandleFunc("GET /v1/tournaments/public/live-arena", s.handlePublicLiveArena)
	mux.HandleFunc("POST /v1/tournaments/public/live-arena", s.handlePublicLiveArena)
	mux.HandleFunc("GET /v1/tournaments/public/score-live", s.handlePublicScoreLive)
	mux.HandleFunc("POST /v1/tournaments/public/score-live", s.handlePublicScoreLive)

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

func (s *Server) requireFacility(ac *authCtx, facilityID int64) error {
	if ac.ViaKey || ac.Principal == nil {
		return nil
	}
	if !ac.Principal.CanAccessFacility(facilityID) {
		return &authError{"forbidden_facility"}
	}
	return nil
}

func (s *Server) tournamentFacility(w http.ResponseWriter, r *http.Request, ac *authCtx) (*tournaments.Tournament, bool) {
	id, err := pathInt(r, "tournamentId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_tournament_id"})
		return nil, false
	}
	row, err := s.Tournaments.Get(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return nil, false
	}
	if err := s.requireFacility(ac, row.FacilityID); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return nil, false
	}
	return row, true
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ok := true
	if err := s.Tournaments.DB.Ping(); err != nil {
		ok = false
	}
	status := http.StatusOK
	st := "ok"
	if !ok {
		status = http.StatusServiceUnavailable
		st = "degraded"
	}
	writeJSON(w, status, map[string]interface{}{
		"service":         "tournaments",
		"status":          st,
		"postgres":        ok,
		"service_key_set": s.Cfg.ServiceKey != "",
		"jwt_ready":       s.JWT != nil,
		"runtime":         "go",
	})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	list, err := s.Tournaments.List(r.Context(), fid, status)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"tournaments": list})
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
	n, err := s.Tournaments.CountActive(r.Context(), fid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"active_count": n})
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := pathInt(r, "facilityId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in tournaments.UpsertInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Tournaments.Create(r.Context(), fid, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	row, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	var in tournaments.UpsertInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Tournaments.Update(r.Context(), cur.FacilityID, cur.ID, in)
	if err != nil {
		code := http.StatusBadRequest
		if err.Error() == "facility_mismatch" {
			code = http.StatusForbidden
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Tournaments.UpdateStatus(r.Context(), cur.FacilityID, cur.ID, body.Status)
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

// --- Phase-2 handlers ---

func (s *Server) handleListEntries(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	list, err := s.Tournaments.ListEntries(r.Context(), cur.ID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"entries": list})
}

func (s *Server) handleCreateEntry(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	var in tournaments.EntryInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Tournaments.CreateEntry(r.Context(), cur.ID, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleEntryPayment(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	entryID, err := pathInt(r, "entryId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_entry_id"})
		return
	}
	entry, err := s.Tournaments.GetEntry(r.Context(), entryID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	t, err := s.Tournaments.Get(r.Context(), entry.TournamentID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	if err := s.requireFacility(ac, t.FacilityID); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var body struct {
		PaymentStatus string `json:"payment_status"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Tournaments.UpdateEntryPayment(r.Context(), entryID, body.PaymentStatus)
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

func (s *Server) handleListFixtures(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	list, err := s.Tournaments.ListFixtures(r.Context(), cur.ID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"fixtures": list})
}

func (s *Server) handleStandings(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	cat := strings.TrimSpace(r.URL.Query().Get("category"))
	gen := strings.TrimSpace(r.URL.Query().Get("gender"))
	tables, err := s.Tournaments.BuildStandings(r.Context(), cur.ID, cat, gen)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":   true,
		"standings": tables,
	})
}

func (s *Server) handleCourtsBoard(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	cat := strings.TrimSpace(r.URL.Query().Get("category"))
	gen := strings.TrimSpace(r.URL.Query().Get("gender"))
	board, err := s.Tournaments.GetCourtsBoard(r.Context(), cur.ID, cat, gen)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":        true,
		"tourId":         board.TourID,
		"courts":         board.Courts,
		"conflicts":      board.Conflicts,
		"bracket_locked": board.BracketLocked,
	})
}

func (s *Server) handleCourtsBoardPost(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	var body struct {
		Category string `json:"category"`
		Gender   string `json:"gender"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	cat := strings.TrimSpace(body.Category)
	if cat == "" {
		cat = strings.TrimSpace(r.URL.Query().Get("category"))
	}
	gen := strings.TrimSpace(body.Gender)
	if gen == "" {
		gen = strings.TrimSpace(r.URL.Query().Get("gender"))
	}
	board, err := s.Tournaments.GetCourtsBoard(r.Context(), cur.ID, cat, gen)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":        true,
		"tourId":         board.TourID,
		"courts":         board.Courts,
		"conflicts":      board.Conflicts,
		"bracket_locked": board.BracketLocked,
	})
}

func (s *Server) handleEntryCheckout(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	entryID, err := pathInt(r, "entryId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_entry_id"})
		return
	}
	entry, err := s.Tournaments.GetEntry(r.Context(), entryID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	t, err := s.Tournaments.Get(r.Context(), entry.TournamentID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	if err := s.requireFacility(ac, t.FacilityID); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in tournaments.CheckoutInput
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	out, err := s.Tournaments.CreateEntryCheckout(r.Context(), entryID, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "checkout": out})
}

func (s *Server) handleEntryConfirmPayment(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	entryID, err := pathInt(r, "entryId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_entry_id"})
		return
	}
	entry, err := s.Tournaments.GetEntry(r.Context(), entryID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	t, err := s.Tournaments.Get(r.Context(), entry.TournamentID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	if err := s.requireFacility(ac, t.FacilityID); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in tournaments.ConfirmPaymentInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Tournaments.ConfirmEntryPayment(r.Context(), entryID, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "entry": row})
}

func (s *Server) handleGatewayPaid(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	// Service-key / internal fan-out only — never trust browser-only callers without auth.
	if !ac.ViaKey && ac.Principal == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var in tournaments.GatewayPaidEvent
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Tournaments.ApplyGatewayPaidEvent(r.Context(), in)
	if err != nil {
		code := http.StatusBadRequest
		if err.Error() == "not_found" {
			code = http.StatusNotFound
		}
		writeJSON(w, code, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "entry": row})
}

func (s *Server) handleGenerateFixtures(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	var in tournaments.GenerateFixturesInput
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	list, err := s.Tournaments.GenerateFixturesEx(r.Context(), cur.ID, in)
	if err != nil {
		code := http.StatusBadRequest
		if err.Error() == "bracket_locked" {
			code = http.StatusLocked
		}
		writeJSON(w, code, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "fixtures": list})
}

func (s *Server) handleFixtureScore(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fixtureID, err := pathInt(r, "fixtureId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_fixture_id"})
		return
	}
	fx, err := s.Tournaments.GetFixture(r.Context(), fixtureID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	t, err := s.Tournaments.Get(r.Context(), fx.TournamentID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	if err := s.requireFacility(ac, t.FacilityID); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var in tournaments.ScoreInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Tournaments.UpdateFixtureScore(r.Context(), fixtureID, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleScoreLive(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	category := strings.TrimSpace(r.URL.Query().Get("category"))
	afterID, _ := strconv.ParseInt(r.URL.Query().Get("after_id"), 10, 64)
	if afterID == 0 {
		afterID, _ = strconv.ParseInt(r.URL.Query().Get("afterId"), 10, 64)
	}
	waitSec, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	events, err := s.Tournaments.PollScoreEvents(r.Context(), cur.ID, category, afterID, waitSec)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	next := afterID
	for _, e := range events {
		if e.ID > next {
			next = e.ID
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true, "tourId": cur.ID, "events": events, "afterId": next,
	})
}

func (s *Server) handleGetTemplate(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	tpl, err := s.Tournaments.GetTemplate(r.Context(), cur.ID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if tpl == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"template": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"template": tpl})
}

func (s *Server) handleSaveTemplate(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	var in tournaments.TemplateInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	tpl, err := s.Tournaments.SaveTemplate(r.Context(), cur.ID, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, tpl)
}

func (s *Server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	if err := s.Tournaments.DeleteTemplate(r.Context(), cur.ID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	row, err := s.Tournaments.Publish(r.Context(), cur.FacilityID, cur.ID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleListRefereesFacility(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fid, err := strconv.ParseInt(r.URL.Query().Get("facility_id"), 10, 64)
	if err != nil || fid <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_facility_id"})
		return
	}
	if err := s.requireFacility(ac, fid); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	list, err := s.Tournaments.ListRefereesByFacility(r.Context(), fid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"referees": list})
}

func (s *Server) handleAssignCourt(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	var in tournaments.AssignCourtInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	res, err := s.Tournaments.AssignRefereeCourtWithPIN(r.Context(), cur.ID, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	out := map[string]interface{}{
		"success": true, "access_code": res.AccessCode, "token": res.Token,
		"expires_at": res.ExpiresAt, "url": res.URL, "whatsapp_sent": res.WhatsAppSent,
	}
	if res.Referee != nil {
		out["id"] = res.Referee.ID
		out["referee"] = res.Referee
		out["tournament_id"] = res.Referee.TournamentID
		out["category"] = res.Referee.Category
		out["court"] = res.Referee.Court
		out["email"] = res.Referee.Email
		out["ref_id"] = res.Referee.RefID
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUnassignCourt(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	var in tournaments.AssignCourtInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if err := s.Tournaments.UnassignRefereeCourt(r.Context(), cur.ID, in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *Server) handleRefereesCourt(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	category := strings.TrimSpace(r.URL.Query().Get("category"))
	court := strings.TrimSpace(r.URL.Query().Get("court"))
	list, err := s.Tournaments.ListRefereesByCourt(r.Context(), cur.ID, category, court)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"referees": list})
}

// --- Advanced handlers ---

func (s *Server) handleGenerateKnockout(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	var in tournaments.KnockoutInput
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	list, err := s.Tournaments.GenerateKnockout(r.Context(), cur.ID, in)
	if err != nil {
		code := http.StatusBadRequest
		if err.Error() == "bracket_locked" {
			code = http.StatusLocked
		}
		writeJSON(w, code, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "fixtures": list})
}

func (s *Server) handleGenerateNextRound(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	var in tournaments.KnockoutInput
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	list, err := s.Tournaments.GenerateNextRound(r.Context(), cur.ID, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "fixtures": list})
}

func (s *Server) handleFixturePreview(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	var in tournaments.PreviewInput
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	prev, err := s.Tournaments.FixturePreview(r.Context(), cur.ID, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, prev)
}

func (s *Server) handleWalkover(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	fixtureID, err := pathInt(r, "fixtureId")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_fixture_id"})
		return
	}
	fx, err := s.Tournaments.GetFixture(r.Context(), fixtureID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	t, err := s.Tournaments.Get(r.Context(), fx.TournamentID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	if err := s.requireFacility(ac, t.FacilityID); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var body tournaments.WalkoverInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Tournaments.Walkover(r.Context(), fixtureID, body.WinnerSide)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleResetFixtures(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	var in tournaments.ResetFixturesInput
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	if err := s.Tournaments.ResetFixtures(r.Context(), cur.ID, in); err != nil {
		code := http.StatusBadRequest
		if err.Error() == "bracket_locked" {
			code = http.StatusLocked
		}
		writeJSON(w, code, map[string]interface{}{"success": false, "error": err.Error(), "requires_force": err.Error() == "bracket_locked"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *Server) handlePlayersTemplate(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	csv, fields := s.Tournaments.PlayersTemplateCSV(r.Context(), cur.ID)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"csv":     csv,
		"fields":  fields,
	})
}

func (s *Server) handlePlayersImport(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	var in tournaments.ImportPlayersInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	list, err := s.Tournaments.ImportPlayers(r.Context(), cur.ID, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"entries": list,
		"count":   len(list),
	})
}

func (s *Server) handleLedger(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	data, err := s.Tournaments.Ledger(r.Context(), cur.ID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "data": data})
}

func (s *Server) handleGetFormFields(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	fields, err := s.Tournaments.GetFormFields(r.Context(), cur.ID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if fields == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "fields": []interface{}{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "fields": fields})
}

func (s *Server) handleSaveFormFields(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	var body struct {
		Fields []map[string]interface{} `json:"fields"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if err := s.Tournaments.SaveFormFields(r.Context(), cur.ID, body.Fields); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "fields": body.Fields})
}

func (s *Server) handleNotifyContacts(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	var body struct {
		Message  string `json:"message"`
		Template string `json:"template"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	n, err := s.Tournaments.NotifyContacts(r.Context(), cur.ID, cur.FacilityID, body.Message, body.Template)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"message":  "sent",
		"notified": n,
	})
}

func (s *Server) handleMarkAllPaid(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	var body struct {
		Category string `json:"category"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	n, err := s.Tournaments.MarkAllPaid(r.Context(), cur.ID, body.Category)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "updated": n})
}

func (s *Server) handleAssignMatch(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	var in tournaments.AssignMatchInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	row, err := s.Tournaments.AssignRefereeMatch(r.Context(), cur.ID, in.MatchID, in.RefID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleUnassignMatch(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	var in struct {
		MatchID int64 `json:"match_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if err := s.Tournaments.UnassignRefereeMatch(r.Context(), cur.ID, in.MatchID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *Server) handleOwnerHistory(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	fid, _ := strconv.ParseInt(r.URL.Query().Get("facility_id"), 10, 64)
	if fid <= 0 && ac.Principal != nil && len(ac.Principal.FacilityIDs) > 0 {
		fid = ac.Principal.FacilityIDs[0]
	}
	if fid > 0 {
		if err := s.requireFacility(ac, fid); err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
	}
	list, err := s.Tournaments.OwnerRefereeHistory(r.Context(), fid, limit)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "referees": list})
}

func (s *Server) handleValidateReferee(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	var body struct {
		Email string `json:"email"`
		RefID string `json:"ref_id"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	p, err := s.Tournaments.ValidateRefereeProfile(r.Context(), body.Email, body.RefID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "valid": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true, "valid": true, "referee": p,
		"email": p.Email, "ref_id": p.RefID, "referee_identity": p.RefereeIdentity,
	})
}

func (s *Server) handleCreateRefLink(w http.ResponseWriter, r *http.Request, ac *authCtx) {
	cur, ok := s.tournamentFacility(w, r, ac)
	if !ok {
		return
	}
	var in tournaments.CreateRefLinkInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	res, err := s.Tournaments.CreateRefLink(r.Context(), cur.ID, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"token":   res.Token,
		"url":     res.URL,
	})
}

// --- Public token referee (no auth) ---

func (s *Server) handleRefMatches(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	list, err := s.Tournaments.RefMatches(r.Context(), token)
	if err != nil {
		code := http.StatusBadRequest
		if err.Error() == "missing_token" || strings.Contains(err.Error(), "no rows") {
			code = http.StatusUnauthorized
		}
		writeJSON(w, code, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"matches":  list,
		"fixtures": list,
		"data":     map[string]interface{}{"matches": list, "fixtures": list},
	})
}

func (s *Server) handleRefScore(w http.ResponseWriter, r *http.Request) {
	s.refScoreCommon(w, r, false)
}

func (s *Server) handleRefSave(w http.ResponseWriter, r *http.Request) {
	s.refScoreCommon(w, r, true)
}

func (s *Server) refScoreCommon(w http.ResponseWriter, r *http.Request, complete bool) {
	var body struct {
		Token   string `json:"token"`
		MatchID int64  `json:"match_id"`
		MatchId int64  `json:"matchId"`
		Score1  int    `json:"score1"`
		Score2  int    `json:"score2"`
		Winner  string `json:"winner"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "invalid_json"})
		return
	}
	matchID := body.MatchID
	if matchID == 0 {
		matchID = body.MatchId
	}
	row, err := s.Tournaments.RefScore(r.Context(), body.Token, matchID, body.Score1, body.Score2, complete, body.Winner)
	if err != nil {
		code := http.StatusBadRequest
		if strings.Contains(err.Error(), "no rows") || err.Error() == "missing_token" {
			code = http.StatusUnauthorized
		}
		writeJSON(w, code, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "data": row, "fixture": row})
}

func (s *Server) handleRefHistory(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.Tournaments.RefHistory(r.Context(), token, limit)
	if err != nil {
		code := http.StatusBadRequest
		if err.Error() == "missing_token" || strings.Contains(err.Error(), "no rows") {
			code = http.StatusUnauthorized
		}
		writeJSON(w, code, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"events":  list,
		"data":    map[string]interface{}{"events": list, "history": list},
	})
}

func (s *Server) handlePublicLiveArena(w http.ResponseWriter, r *http.Request) {
	action := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	if action == "" && r.Method == http.MethodPost {
		var body map[string]interface{}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
		if v, ok := body["action"].(string); ok {
			action = strings.ToLower(strings.TrimSpace(v))
		}
		if action == "board" {
			if tid, ok := body["tourId"].(float64); ok {
				r.URL.RawQuery = "action=board&tourId=" + strconv.FormatInt(int64(tid), 10)
			}
			if c, ok := body["category"].(string); ok {
				q := r.URL.Query()
				q.Set("category", c)
				r.URL.RawQuery = q.Encode()
			}
		}
	}
	if action == "" {
		action = "list"
	}
	switch action {
	case "ping":
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "message": "OK", "data": map[string]string{"file": "live-arena"}})
	case "list":
		list, err := s.Tournaments.ListPublicLiveTournaments(r.Context())
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": true, "message": "OK",
			"data": map[string]interface{}{"tournaments": list, "count": len(list)},
		})
	case "board":
		tid, _ := strconv.ParseInt(r.URL.Query().Get("tourId"), 10, 64)
		if tid == 0 {
			tid, _ = strconv.ParseInt(r.URL.Query().Get("tournamentId"), 10, 64)
		}
		cat := strings.TrimSpace(r.URL.Query().Get("category"))
		gen := strings.TrimSpace(r.URL.Query().Get("gender"))
		board, err := s.Tournaments.GetPublicLiveBoard(r.Context(), tid, cat, gen)
		if err != nil {
			code := http.StatusBadRequest
			if err.Error() == "not_found" || err.Error() == "not_public" {
				code = http.StatusNotFound
			}
			writeJSON(w, code, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "message": "OK", "data": board})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "unknown_action"})
	}
}

func (s *Server) handlePublicScoreLive(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if r.Method == http.MethodPost {
		var body map[string]interface{}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
		for _, k := range []string{"tourId", "tournamentId", "after_id", "afterId", "category", "wait", "action"} {
			if v, ok := body[k]; ok {
				q.Set(k, fmt.Sprint(v))
			}
		}
	}
	action := strings.ToLower(strings.TrimSpace(q.Get("action")))
	if action == "ping" || action == "ping_public" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "data": map[string]bool{"ok": true}})
		return
	}
	tid, _ := strconv.ParseInt(q.Get("tourId"), 10, 64)
	if tid == 0 {
		tid, _ = strconv.ParseInt(q.Get("tournamentId"), 10, 64)
	}
	afterID, _ := strconv.ParseInt(q.Get("after_id"), 10, 64)
	if afterID == 0 {
		afterID, _ = strconv.ParseInt(q.Get("afterId"), 10, 64)
	}
	waitSec, _ := strconv.Atoi(q.Get("wait"))
	if waitSec == 0 && (action == "wait" || action == "wait_public") {
		waitSec = 20
	}
	out, err := s.Tournaments.PublicScoreLive(r.Context(), tid, afterID, strings.TrimSpace(q.Get("category")), waitSec)
	if err != nil {
		code := http.StatusBadRequest
		if err.Error() == "not_found" || err.Error() == "not_public" {
			code = http.StatusNotFound
		}
		writeJSON(w, code, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "message": "OK", "data": out, "events": out["events"], "afterId": out["afterId"], "tourId": tid})
}

func (s *Server) handleRefLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
		RefID string `json:"ref_id"`
		RefId string `json:"refId"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "invalid_json"})
		return
	}
	refRaw := strings.TrimSpace(body.RefID)
	if refRaw == "" {
		refRaw = strings.TrimSpace(body.RefId)
	}
	out, err := s.Tournaments.RefereeLogin(r.Context(), body.Email, refRaw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true, "message": "Referee login ok.",
		"data": out, "referee": out.Referee, "owners": out.Owners, "scorecards": out.Scorecards,
	})
}

func (s *Server) handleRefUnlock(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefID          int64  `json:"ref_id"`
		RefId          int64  `json:"refId"`
		AssignmentID   int64  `json:"assignment_id"`
		AssignmentId   int64  `json:"assignmentId"`
		AccessCode     string `json:"access_code"`
		AccessCodeAlt  string `json:"accessCode"`
		Pin            string `json:"pin"`
		Email          string `json:"email"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "invalid_json"})
		return
	}
	refID := body.RefID
	if refID == 0 {
		refID = body.RefId
	}
	if refID == 0 && strings.TrimSpace(body.Email) != "" {
		p, err := s.Tournaments.ValidateRefereeProfile(r.Context(), body.Email, "")
		if err == nil {
			refID = p.RefID
		}
	}
	assignmentID := body.AssignmentID
	if assignmentID == 0 {
		assignmentID = body.AssignmentId
	}
	code := strings.TrimSpace(body.AccessCode)
	if code == "" {
		code = strings.TrimSpace(body.AccessCodeAlt)
	}
	if code == "" {
		code = strings.TrimSpace(body.Pin)
	}
	card, err := s.Tournaments.UnlockRefereeScorecard(r.Context(), refID, assignmentID, code)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "message": "Scorecard unlocked.", "data": card})
}

func (s *Server) handleRefValidateAssignment(w http.ResponseWriter, r *http.Request) {
	refID, _ := strconv.ParseInt(r.URL.Query().Get("ref_id"), 10, 64)
	if refID == 0 {
		refID, _ = strconv.ParseInt(r.URL.Query().Get("refId"), 10, 64)
	}
	assignmentID, _ := strconv.ParseInt(r.URL.Query().Get("assignment_id"), 10, 64)
	if assignmentID == 0 {
		assignmentID, _ = strconv.ParseInt(r.URL.Query().Get("assignmentId"), 10, 64)
	}
	card, err := s.Tournaments.ValidateRefereeAssignment(r.Context(), refID, assignmentID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "valid": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true, "valid": true, "data": card,
		"session_valid": true, "unlocked": card.Unlocked, "token": card.Token,
	})
}

func pathInt(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(r.PathValue(name), 10, 64)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
