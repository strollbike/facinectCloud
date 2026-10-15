package enquiry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/facinect/enquiry/internal/config"
)

type Service struct {
	DB     *sql.DB
	Cfg    config.Config
	Client *http.Client
}

func New(db *sql.DB, cfg config.Config) *Service {
	return &Service{
		DB:  db,
		Cfg: cfg,
		Client: &http.Client{
			Timeout: 50 * time.Second,
		},
	}
}

type EnquiryRow struct {
	ID             int64                  `json:"id"`
	FacilityID     int64                  `json:"facility_id"`
	CustomerPhone  string                 `json:"customer_phone"`
	CustomerName   string                 `json:"customer_name,omitempty"`
	EnquiryDetails string                 `json:"enquiry_details"`
	Status         string                 `json:"status"`
	AIData         map[string]interface{} `json:"ai_data,omitempty"`
	CreatedAt      string                 `json:"created_at,omitempty"`
	UpdatedAt      string                 `json:"updated_at,omitempty"`
}

type CreateInput struct {
	CustomerPhone  string                 `json:"customer_phone"`
	CustomerName   string                 `json:"customer_name"`
	EnquiryDetails string                 `json:"enquiry_details"`
	AIData         map[string]interface{} `json:"ai_data"`
	RunAI          *bool                  `json:"run_ai"`
}

func (s *Service) List(ctx context.Context, facilityID int64, status, search string, limit, offset int) ([]EnquiryRow, int, error) {
	if facilityID <= 0 {
		return nil, 0, fmt.Errorf("invalid_facility")
	}
	if limit <= 0 {
		limit = 40
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	where := `facility_id=$1`
	args := []interface{}{facilityID}
	n := 2
	st := strings.TrimSpace(status)
	if st != "" && !strings.EqualFold(st, "All") {
		where += fmt.Sprintf(` AND status=$%d`, n)
		args = append(args, st)
		n++
	}
	search = strings.TrimSpace(search)
	if search != "" {
		where += fmt.Sprintf(` AND customer_phone LIKE $%d`, n)
		args = append(args, "%"+search+"%")
		n++
	}

	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM enquiries WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := fmt.Sprintf(`
SELECT id, facility_id, customer_phone, COALESCE(customer_name,''), enquiry_details, status,
       ai_data, created_at::text, updated_at::text
FROM enquiries WHERE %s
ORDER BY created_at DESC
LIMIT %d OFFSET %d`, where, limit, offset)

	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []EnquiryRow{}
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *r)
	}
	return out, total, rows.Err()
}

func (s *Service) CountNew(ctx context.Context, facilityID int64) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM enquiries WHERE facility_id=$1 AND status='New'`, facilityID).Scan(&n)
	return n, err
}

func (s *Service) Get(ctx context.Context, id int64) (*EnquiryRow, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, facility_id, customer_phone, COALESCE(customer_name,''), enquiry_details, status,
       ai_data, created_at::text, updated_at::text
FROM enquiries WHERE id=$1`, id)
	return scanRow(row)
}

func (s *Service) Create(ctx context.Context, facilityID int64, in CreateInput) (*EnquiryRow, error) {
	phone := strings.TrimSpace(in.CustomerPhone)
	details := strings.TrimSpace(in.EnquiryDetails)
	if facilityID <= 0 || phone == "" || details == "" {
		return nil, fmt.Errorf("invalid_enquiry")
	}
	runAI := true
	if in.RunAI != nil {
		runAI = *in.RunAI
	}
	ai := in.AIData
	if runAI && ai == nil {
		analyzed, err := s.callAIAnalyze(ctx, details)
		if err != nil {
			return nil, err
		}
		ai = analyzed
	}
	var aiRaw interface{}
	if ai != nil {
		b, err := json.Marshal(ai)
		if err != nil {
			return nil, err
		}
		aiRaw = string(b)
	}
	var id int64
	err := s.DB.QueryRowContext(ctx, `
INSERT INTO enquiries (facility_id, customer_phone, customer_name, enquiry_details, status, ai_data)
VALUES ($1,$2,NULLIF($3,''),$4,'New',$5::jsonb)
RETURNING id`,
		facilityID, phone, strings.TrimSpace(in.CustomerName), details, aiRaw,
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *Service) UpdateStatus(ctx context.Context, facilityID, id int64, status string) (*EnquiryRow, error) {
	st := normalizeStatus(status)
	if st == "" {
		return nil, fmt.Errorf("invalid_status")
	}
	res, err := s.DB.ExecContext(ctx, `
UPDATE enquiries SET status=$1, updated_at=NOW()
WHERE id=$2 AND facility_id=$3`, st, id, facilityID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("not_found")
	}
	return s.Get(ctx, id)
}

func (s *Service) Reanalyze(ctx context.Context, facilityID, id int64) (*EnquiryRow, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if cur.FacilityID != facilityID {
		return nil, fmt.Errorf("facility_mismatch")
	}
	ai, err := s.callAIAnalyze(ctx, cur.EnquiryDetails)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(ai)
	if err != nil {
		return nil, err
	}
	_, err = s.DB.ExecContext(ctx, `
UPDATE enquiries SET ai_data=$1::jsonb, updated_at=NOW() WHERE id=$2 AND facility_id=$3`,
		string(b), id, facilityID)
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *Service) SuggestReply(ctx context.Context, facilityName string, row *EnquiryRow) (string, error) {
	return s.callAISuggest(ctx, facilityName, row.CustomerName, row.AIData)
}

func (s *Service) Notify(ctx context.Context, facilityID int64, row *EnquiryRow, message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return fmt.Errorf("message_required")
	}
	payload := map[string]interface{}{
		"facility_id": facilityID,
		"channel":     "whatsapp",
		"template":    "enquiry_response",
		"to": map[string]string{
			"whatsapp": row.CustomerPhone,
		},
		"data": map[string]interface{}{
			"body":        message,
			"text":        message,
			"body_params": []string{message},
		},
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Cfg.NotificationsBaseURL+"/v1/notifications/send", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Cfg.NotificationsKey != "" {
		req.Header.Set("X-Service-Key", s.Cfg.NotificationsKey)
	}
	res, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("notify_http_%d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	_, _ = s.UpdateStatus(ctx, facilityID, row.ID, "Follow-up")
	return nil
}

func (s *Service) callAIAnalyze(ctx context.Context, transcript string) (map[string]interface{}, error) {
	payload := map[string]string{"transcript": transcript}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Cfg.AIBaseURL+"/v1/ai/enquiry/analyze", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Cfg.AIServiceKey != "" {
		req.Header.Set("X-Service-Key", s.Cfg.AIServiceKey)
	}
	res, err := s.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ai_unreachable: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("ai_http_%d", res.StatusCode)
	}
	var out struct {
		AIData map[string]interface{} `json:"ai_data"`
		Error  string                 `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if out.Error != "" {
		return nil, fmt.Errorf("%s", out.Error)
	}
	if out.AIData == nil {
		return nil, fmt.Errorf("ai_empty")
	}
	return out.AIData, nil
}

func (s *Service) callAISuggest(ctx context.Context, facilityName, customerName string, ai map[string]interface{}) (string, error) {
	payload := map[string]interface{}{
		"facility_name": facilityName,
		"customer_name": customerName,
		"ai_data":       ai,
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Cfg.AIBaseURL+"/v1/ai/reply/suggest", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Cfg.AIServiceKey != "" {
		req.Header.Set("X-Service-Key", s.Cfg.AIServiceKey)
	}
	res, err := s.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("ai_http_%d", res.StatusCode)
	}
	var out struct {
		Reply string `json:"reply"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.Error != "" {
		return "", fmt.Errorf("%s", out.Error)
	}
	return out.Reply, nil
}

type scannable interface {
	Scan(dest ...interface{}) error
}

func scanRow(row scannable) (*EnquiryRow, error) {
	var r EnquiryRow
	var aiRaw sql.NullString
	err := row.Scan(
		&r.ID, &r.FacilityID, &r.CustomerPhone, &r.CustomerName, &r.EnquiryDetails, &r.Status,
		&aiRaw, &r.CreatedAt, &r.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if aiRaw.Valid && aiRaw.String != "" && aiRaw.String != "null" {
		_ = json.Unmarshal([]byte(aiRaw.String), &r.AIData)
	}
	return &r, nil
}

func normalizeStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "New", "Follow-up", "Resolved", "Closed":
		return strings.TrimSpace(status)
	case "new":
		return "New"
	case "follow-up", "followup", "Followup":
		return "Follow-up"
	case "resolved":
		return "Resolved"
	case "closed":
		return "Closed"
	default:
		return ""
	}
}
