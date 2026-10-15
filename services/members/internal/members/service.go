package members

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/facinect/members/internal/config"
)

type Service struct {
	DB  *sql.DB
	Cfg config.Config
}

type MemberRow struct {
	MembershipID   int64    `json:"membership_id"`
	MemberID       int64    `json:"member_id"`
	FacilityID     int64    `json:"facility_id"`
	FullName       string   `json:"full_name"`
	Email          string   `json:"contact_email,omitempty"`
	Phone          string   `json:"contact_phone,omitempty"`
	WhatsApp       string   `json:"whatsapp,omitempty"`
	SportID        *int64   `json:"sport_id,omitempty"`
	PlanName       string   `json:"plan_name,omitempty"`
	TeamName       string   `json:"team_name,omitempty"`
	StartDate      string   `json:"start_date,omitempty"`
	EndDate        string   `json:"end_date,omitempty"`
	SubscriptionFee *float64 `json:"subscription_fee,omitempty"`
	PrimaryMember  bool     `json:"primary_member"`
	Status         string   `json:"status"`
	PaymentStatus  string   `json:"payment_status,omitempty"`
	PlanID         *int64   `json:"plan_id,omitempty"`
	CreatedAt      string   `json:"created_at,omitempty"`
}

type RegisterInput struct {
	FullName        string   `json:"full_name"`
	Email           string   `json:"contact_email"`
	Phone           string   `json:"contact_phone"`
	WhatsApp        string   `json:"whatsapp"`
	SportID         *int64   `json:"sport_id"`
	PlanName        string   `json:"plan_name"`
	TeamName        string   `json:"team_name"`
	StartDate       string   `json:"start_date"`
	EndDate         string   `json:"end_date"`
	SubscriptionFee *float64 `json:"subscription_fee"`
	PrimaryMember   bool     `json:"primary_member"`
}

type UpdateStatusInput struct {
	Status string `json:"status"`
}

type UpdateProfileInput struct {
	FullName      string `json:"full_name"`
	WhatsApp      string `json:"whatsapp"`
	Phone         string `json:"contact_phone"`
	TeamName      string `json:"team_name"`
	Status        string `json:"status"`
	PrimaryMember *bool  `json:"primary_member"`
}

func (s *Service) ListMembers(ctx context.Context, facilityID int64, sportID *int64, status string) ([]MemberRow, error) {
	if facilityID <= 0 {
		return nil, fmt.Errorf("invalid_facility")
	}
	q := `
SELECT mship.id, m.id, mship.facility_id, m.full_name,
       COALESCE(m.contact_email,''), COALESCE(m.contact_phone,''), COALESCE(m.whatsapp,''),
       mship.sport_id, COALESCE(mship.plan_name,''), COALESCE(mship.team_name,''),
       COALESCE(mship.start_date::text,''), COALESCE(mship.end_date::text,''),
       mship.subscription_fee, mship.primary_member, mship.status,
       COALESCE(mship.payment_status,'unpaid'), mship.plan_id, mship.created_at::text
FROM memberships mship
JOIN members m ON m.id = mship.member_id
WHERE mship.facility_id=$1`
	args := []interface{}{facilityID}
	argN := 2
	if sportID != nil && *sportID > 0 {
		q += fmt.Sprintf(` AND mship.sport_id=$%d`, argN)
		args = append(args, *sportID)
		argN++
	}
	st := strings.TrimSpace(strings.ToLower(status))
	if st != "" && st != "all" {
		q += fmt.Sprintf(` AND mship.status=$%d`, argN)
		args = append(args, st)
	}
	q += ` ORDER BY m.full_name, mship.id DESC`

	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MemberRow{}
	for rows.Next() {
		var r MemberRow
		var sport, planID sql.NullInt64
		var fee sql.NullFloat64
		if err := rows.Scan(
			&r.MembershipID, &r.MemberID, &r.FacilityID, &r.FullName,
			&r.Email, &r.Phone, &r.WhatsApp, &sport, &r.PlanName, &r.TeamName,
			&r.StartDate, &r.EndDate, &fee, &r.PrimaryMember, &r.Status,
			&r.PaymentStatus, &planID, &r.CreatedAt,
		); err != nil {
			return nil, err
		}
		if sport.Valid {
			v := sport.Int64
			r.SportID = &v
		}
		if fee.Valid {
			v := fee.Float64
			r.SubscriptionFee = &v
		}
		if planID.Valid {
			v := planID.Int64
			r.PlanID = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) CountActive(ctx context.Context, facilityID int64) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM memberships
WHERE facility_id=$1 AND status='active'`, facilityID).Scan(&n)
	return n, err
}

func (s *Service) Register(ctx context.Context, facilityID int64, in RegisterInput) (*MemberRow, error) {
	name := strings.TrimSpace(in.FullName)
	if facilityID <= 0 || name == "" {
		return nil, fmt.Errorf("invalid_member")
	}
	if in.StartDate != "" {
		if err := validateDate(in.StartDate); err != nil {
			return nil, err
		}
	}
	if in.EndDate != "" {
		if err := validateDate(in.EndDate); err != nil {
			return nil, err
		}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	wa := strings.TrimSpace(in.WhatsApp)
	if wa == "" {
		wa = strings.TrimSpace(in.Phone)
	}

	var memberID int64
	err = tx.QueryRowContext(ctx, `
INSERT INTO members (full_name, contact_email, contact_phone, whatsapp, status)
VALUES ($1,NULLIF($2,''),NULLIF($3,''),NULLIF($4,''),'active')
RETURNING id`,
		name,
		strings.TrimSpace(in.Email),
		strings.TrimSpace(in.Phone),
		wa,
	).Scan(&memberID)
	if err != nil {
		return nil, err
	}

	var sport, start, end, fee interface{}
	if in.SportID != nil {
		sport = *in.SportID
	}
	if strings.TrimSpace(in.StartDate) != "" {
		start = in.StartDate
	}
	if strings.TrimSpace(in.EndDate) != "" {
		end = in.EndDate
	}
	if in.SubscriptionFee != nil {
		fee = *in.SubscriptionFee
	}

	var membershipID int64
	err = tx.QueryRowContext(ctx, `
INSERT INTO memberships
  (facility_id, member_id, sport_id, plan_name, team_name, start_date, end_date,
   subscription_fee, primary_member, status)
VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),$6::date,$7::date,$8,$9,'active')
RETURNING id`,
		facilityID, memberID, sport,
		strings.TrimSpace(in.PlanName),
		strings.TrimSpace(in.TeamName),
		start, end, fee, in.PrimaryMember,
	).Scan(&membershipID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	row, err := s.GetMembership(ctx, membershipID)
	if err != nil {
		return nil, err
	}
	// Best-effort Meta template (PHP member_edit.php parity).
	s.notifyMemberRegistrationConfirmation(ctx, row)
	return row, nil
}

func (s *Service) GetMembership(ctx context.Context, membershipID int64) (*MemberRow, error) {
	var r MemberRow
	var sport, planID sql.NullInt64
	var fee sql.NullFloat64
	err := s.DB.QueryRowContext(ctx, `
SELECT mship.id, m.id, mship.facility_id, m.full_name,
       COALESCE(m.contact_email,''), COALESCE(m.contact_phone,''), COALESCE(m.whatsapp,''),
       mship.sport_id, COALESCE(mship.plan_name,''), COALESCE(mship.team_name,''),
       COALESCE(mship.start_date::text,''), COALESCE(mship.end_date::text,''),
       mship.subscription_fee, mship.primary_member, mship.status,
       COALESCE(mship.payment_status,'unpaid'), mship.plan_id, mship.created_at::text
FROM memberships mship
JOIN members m ON m.id = mship.member_id
WHERE mship.id=$1`, membershipID).Scan(
		&r.MembershipID, &r.MemberID, &r.FacilityID, &r.FullName,
		&r.Email, &r.Phone, &r.WhatsApp, &sport, &r.PlanName, &r.TeamName,
		&r.StartDate, &r.EndDate, &fee, &r.PrimaryMember, &r.Status,
		&r.PaymentStatus, &planID, &r.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	if sport.Valid {
		v := sport.Int64
		r.SportID = &v
	}
	if fee.Valid {
		v := fee.Float64
		r.SubscriptionFee = &v
	}
	if planID.Valid {
		v := planID.Int64
		r.PlanID = &v
	}
	return &r, nil
}

func (s *Service) UpdateMembershipStatus(ctx context.Context, facilityID, membershipID int64, status string) (*MemberRow, error) {
	st := strings.ToLower(strings.TrimSpace(status))
	if st != "active" && st != "inactive" && st != "expired" {
		return nil, fmt.Errorf("invalid_status")
	}
	res, err := s.DB.ExecContext(ctx, `
UPDATE memberships SET status=$1, updated_at=NOW()
WHERE id=$2 AND facility_id=$3`, st, membershipID, facilityID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("not_found")
	}
	return s.GetMembership(ctx, membershipID)
}

// UpdateMembershipProfile updates member contact fields + membership team/primary/status.
func (s *Service) UpdateMembershipProfile(ctx context.Context, facilityID, membershipID int64, in UpdateProfileInput) (*MemberRow, error) {
	cur, err := s.GetMembership(ctx, membershipID)
	if err != nil {
		return nil, fmt.Errorf("not_found")
	}
	if cur.FacilityID != facilityID {
		return nil, fmt.Errorf("facility_mismatch")
	}

	name := strings.TrimSpace(in.FullName)
	if name == "" {
		name = cur.FullName
	}
	wa := strings.TrimSpace(in.WhatsApp)
	if wa == "" {
		wa = strings.TrimSpace(in.Phone)
	}
	if wa == "" {
		wa = cur.WhatsApp
	}
	phone := strings.TrimSpace(in.Phone)
	if phone == "" {
		phone = cur.Phone
	}
	if phone == "" {
		phone = wa
	}
	team := strings.TrimSpace(in.TeamName)
	// Allow clearing team with empty string when key present — keep existing if omitted empty and we can't tell.
	// Callers always send team_name; empty clears.
	st := strings.ToLower(strings.TrimSpace(in.Status))
	if st == "" {
		st = cur.Status
	}
	if st != "active" && st != "inactive" && st != "expired" {
		return nil, fmt.Errorf("invalid_status")
	}
	primary := cur.PrimaryMember
	if in.PrimaryMember != nil {
		primary = *in.PrimaryMember
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
UPDATE members SET
  full_name=$1,
  contact_phone=NULLIF($2,''),
  whatsapp=NULLIF($3,''),
  updated_at=NOW()
WHERE id=$4`, name, phone, wa, cur.MemberID)
	if err != nil {
		return nil, err
	}

	if primary {
		// One primary membership per member within facility.
		_, err = tx.ExecContext(ctx, `
UPDATE memberships SET primary_member=FALSE, updated_at=NOW()
WHERE facility_id=$1 AND member_id=$2 AND id<>$3 AND primary_member=TRUE`,
			facilityID, cur.MemberID, membershipID)
		if err != nil {
			return nil, err
		}
	}

	res, err := tx.ExecContext(ctx, `
UPDATE memberships SET
  team_name=NULLIF($1,''),
  primary_member=$2,
  status=$3,
  updated_at=NOW()
WHERE id=$4 AND facility_id=$5`,
		team, primary, st, membershipID, facilityID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("not_found")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetMembership(ctx, membershipID)
}

func validateDate(date string) error {
	date = strings.TrimSpace(date)
	if date == "" {
		return fmt.Errorf("date_required")
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return fmt.Errorf("invalid_date")
	}
	return nil
}
