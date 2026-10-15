package booking

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/facinect/booking/internal/config"
)

type Service struct {
	DB  *sql.DB
	Cfg config.Config
}

type Court struct {
	ID         int64  `json:"id"`
	FacilityID int64  `json:"facility_id"`
	Name       string `json:"name"`
	SportID    *int64 `json:"sport_id,omitempty"`
	Status     string `json:"status"`
}

type Slot struct {
	ID            int64  `json:"id"`
	FacilityID    int64  `json:"facility_id"`
	CourtID       int64  `json:"court_id"`
	CourtName     string `json:"court_name,omitempty"`
	SlotDate      string `json:"slot_date"`
	StartTime     string `json:"start_time"`
	EndTime       string `json:"end_time"`
	Status        string `json:"status"`
	Notes         string `json:"notes,omitempty"`
	CustomerName  string `json:"customer_name,omitempty"`
	CustomerPhone string `json:"customer_phone,omitempty"`
}

type Booking struct {
	ID             int64    `json:"id"`
	FacilityID     int64    `json:"facility_id"`
	SlotID         int64    `json:"slot_id"`
	UserID         *int64   `json:"user_id,omitempty"`
	CustomerName   string   `json:"customer_name,omitempty"`
	CustomerPhone  string   `json:"customer_phone,omitempty"`
	CustomerEmail  string   `json:"customer_email,omitempty"`
	Status         string   `json:"status"`
	Notes          string   `json:"notes,omitempty"`
	PaymentStatus  string   `json:"payment_status,omitempty"`
	PayMode        string   `json:"pay_mode,omitempty"`
	AmountPaid     *float64 `json:"amount_paid,omitempty"`
	SlotDate       string   `json:"slot_date,omitempty"`
	StartTime      string   `json:"start_time,omitempty"`
	EndTime        string   `json:"end_time,omitempty"`
	CourtName      string   `json:"court_name,omitempty"`
	CreatedAt      string   `json:"created_at,omitempty"`
}

type CreateCourtInput struct {
	Name    string `json:"name"`
	SportID *int64 `json:"sport_id"`
}

type GenerateSlotsInput struct {
	Date        string `json:"date"`
	DateTo      string `json:"date_to"`
	MonthWindow *bool  `json:"month_window"` // Rolling 30 days: today → today+29
	CourtID     *int64 `json:"court_id"`
	OpenHour    *int   `json:"open_hour"`
	CloseHour   *int   `json:"close_hour"`
	SlotMinutes *int   `json:"slot_minutes"`
	// Replace=true: wipe Free slots in range and recreate. Aborts if any booked/blocked exist.
	Replace *bool `json:"replace"`
}

type CreateBookingInput struct {
	SlotID        int64  `json:"slot_id"`
	UserID        *int64 `json:"user_id"`
	CustomerName  string `json:"customer_name"`
	CustomerPhone string `json:"customer_phone"`
	CustomerEmail string `json:"customer_email"`
	Notes         string `json:"notes"`
}

func (s *Service) CreateCourt(ctx context.Context, facilityID int64, in CreateCourtInput) (*Court, error) {
	name := strings.TrimSpace(in.Name)
	if facilityID <= 0 || name == "" {
		return nil, fmt.Errorf("invalid_court")
	}
	var sport interface{}
	if in.SportID != nil {
		sport = *in.SportID
	}
	var id int64
	err := s.DB.QueryRowContext(ctx, `
INSERT INTO booking_courts (facility_id, name, sport_id, status)
VALUES ($1, $2, $3, 'active') RETURNING id`, facilityID, name, sport).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.GetCourt(ctx, id)
}

func (s *Service) ListCourts(ctx context.Context, facilityID int64) ([]Court, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, facility_id, name, sport_id, status
FROM booking_courts WHERE facility_id=$1 AND status='active' ORDER BY name`, facilityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Court{}
	for rows.Next() {
		var c Court
		var sport sql.NullInt64
		if err := rows.Scan(&c.ID, &c.FacilityID, &c.Name, &sport, &c.Status); err != nil {
			return nil, err
		}
		if sport.Valid {
			v := sport.Int64
			c.SportID = &v
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) GetCourt(ctx context.Context, id int64) (*Court, error) {
	var c Court
	var sport sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `
SELECT id, facility_id, name, sport_id, status FROM booking_courts WHERE id=$1`, id).
		Scan(&c.ID, &c.FacilityID, &c.Name, &sport, &c.Status)
	if err != nil {
		return nil, err
	}
	if sport.Valid {
		v := sport.Int64
		c.SportID = &v
	}
	return &c, nil
}

func (s *Service) ListSlots(ctx context.Context, facilityID int64, date string, courtID *int64) ([]Slot, error) {
	if err := validateDate(date); err != nil {
		return nil, err
	}
	q := `
SELECT s.id, s.facility_id, s.court_id, c.name, s.slot_date::text, s.start_time::text, s.end_time::text,
       s.status, COALESCE(s.notes,''),
       COALESCE(NULLIF(bk.customer_name,''), NULLIF(hold.customer_name,''), ''),
       COALESCE(NULLIF(bk.customer_phone,''), NULLIF(hold.whatsapp,''), '')
FROM booking_slots s
JOIN booking_courts c ON c.id = s.court_id
LEFT JOIN LATERAL (
  SELECT COALESCE(b.customer_name,'') AS customer_name,
         COALESCE(b.customer_phone,'') AS customer_phone
  FROM booking_bookings b
  WHERE b.slot_id = s.id AND b.status = 'confirmed'
  ORDER BY b.id DESC
  LIMIT 1
) bk ON TRUE
LEFT JOIN LATERAL (
  SELECT COALESCE(h.customer_name,'') AS customer_name,
         COALESCE(h.whatsapp,'') AS whatsapp
  FROM booking_payment_holds h
  WHERE h.status = 'pending' AND s.id = ANY(h.slot_ids)
  ORDER BY h.id DESC
  LIMIT 1
) hold ON TRUE
WHERE s.facility_id=$1 AND s.slot_date=$2::date`
	args := []interface{}{facilityID, date}
	if courtID != nil && *courtID > 0 {
		q += ` AND s.court_id=$3`
		args = append(args, *courtID)
	}
	q += ` ORDER BY c.name, s.start_time`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSlots(rows)
}


func (s *Service) BlockSlot(ctx context.Context, slotID int64, notes string) (*Slot, error) {
	return s.setSlotStatus(ctx, slotID, "available", "blocked", notes)
}

func (s *Service) UnblockSlot(ctx context.Context, slotID int64) (*Slot, error) {
	return s.setSlotStatus(ctx, slotID, "blocked", "available", "")
}

func (s *Service) setSlotStatus(ctx context.Context, slotID int64, from, to, notes string) (*Slot, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var id, facilityID, courtID int64
	var status string
	err = tx.QueryRowContext(ctx, `
SELECT id, facility_id, court_id, status FROM booking_slots WHERE id=$1 FOR UPDATE`, slotID).
		Scan(&id, &facilityID, &courtID, &status)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("slot_not_found")
	}
	if err != nil {
		return nil, err
	}
	if status != from {
		return nil, fmt.Errorf("invalid_slot_status")
	}
	_, err = tx.ExecContext(ctx, `
UPDATE booking_slots SET status=$2, notes=NULLIF($3,''), updated_at=NOW() WHERE id=$1`, slotID, to, notes)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetSlot(ctx, slotID)
}

func (s *Service) GetSlot(ctx context.Context, slotID int64) (*Slot, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT s.id, s.facility_id, s.court_id, c.name, s.slot_date::text, s.start_time::text, s.end_time::text,
       s.status, COALESCE(s.notes,'')
FROM booking_slots s
JOIN booking_courts c ON c.id = s.court_id
WHERE s.id=$1`, slotID)
	slots, err := scanSlotRows(row)
	if err != nil {
		return nil, err
	}
	return &slots[0], nil
}

func (s *Service) CreateBooking(ctx context.Context, facilityID int64, in CreateBookingInput) (*Booking, error) {
	if facilityID <= 0 || in.SlotID <= 0 {
		return nil, fmt.Errorf("invalid_booking")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var slotFacility int64
	var status string
	err = tx.QueryRowContext(ctx, `
SELECT facility_id, status FROM booking_slots WHERE id=$1 FOR UPDATE`, in.SlotID).
		Scan(&slotFacility, &status)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("slot_not_found")
	}
	if err != nil {
		return nil, err
	}
	if slotFacility != facilityID {
		return nil, fmt.Errorf("facility_mismatch")
	}
	if status != "available" {
		return nil, fmt.Errorf("slot_not_available")
	}

	var userID interface{}
	if in.UserID != nil {
		userID = *in.UserID
	}
	var bookingID int64
	err = tx.QueryRowContext(ctx, `
INSERT INTO booking_bookings (
  facility_id, slot_id, user_id, customer_name, customer_phone, customer_email, status, notes
) VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),'confirmed',NULLIF($7,''))
RETURNING id`,
		facilityID, in.SlotID, userID,
		strings.TrimSpace(in.CustomerName),
		strings.TrimSpace(in.CustomerPhone),
		strings.TrimSpace(in.CustomerEmail),
		strings.TrimSpace(in.Notes),
	).Scan(&bookingID)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
UPDATE booking_slots SET status='booked', notes=NULLIF($2,''), updated_at=NOW() WHERE id=$1`,
		in.SlotID, strings.TrimSpace(in.Notes))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetBooking(ctx, bookingID)
}

func (s *Service) CancelBooking(ctx context.Context, bookingID int64) (*Booking, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var slotID int64
	var status string
	err = tx.QueryRowContext(ctx, `
SELECT slot_id, status FROM booking_bookings WHERE id=$1 FOR UPDATE`, bookingID).
		Scan(&slotID, &status)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("booking_not_found")
	}
	if err != nil {
		return nil, err
	}
	if status != "confirmed" {
		return nil, fmt.Errorf("booking_not_cancellable")
	}
	_, err = tx.ExecContext(ctx, `
UPDATE booking_bookings SET status='cancelled', updated_at=NOW() WHERE id=$1`, bookingID)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
UPDATE booking_slots SET status='available', updated_at=NOW()
WHERE id=$1 AND status='booked'`, slotID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetBooking(ctx, bookingID)
}

func (s *Service) GetBooking(ctx context.Context, id int64) (*Booking, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT b.id, b.facility_id, b.slot_id, b.user_id, COALESCE(b.customer_name,''), COALESCE(b.customer_phone,''),
       COALESCE(b.customer_email,''), b.status, COALESCE(b.notes,''),
       COALESCE(b.payment_status,'unpaid'), COALESCE(b.pay_mode,''), b.amount_paid,
       s.slot_date::text, s.start_time::text, s.end_time::text, c.name, b.created_at::text
FROM booking_bookings b
JOIN booking_slots s ON s.id = b.slot_id
JOIN booking_courts c ON c.id = s.court_id
WHERE b.id=$1`, id)
	var b Booking
	var userID sql.NullInt64
	var amount sql.NullFloat64
	if err := row.Scan(
		&b.ID, &b.FacilityID, &b.SlotID, &userID, &b.CustomerName, &b.CustomerPhone,
		&b.CustomerEmail, &b.Status, &b.Notes, &b.PaymentStatus, &b.PayMode, &amount,
		&b.SlotDate, &b.StartTime, &b.EndTime, &b.CourtName, &b.CreatedAt,
	); err != nil {
		return nil, err
	}
	if userID.Valid {
		v := userID.Int64
		b.UserID = &v
	}
	if amount.Valid {
		v := amount.Float64
		b.AmountPaid = &v
	}
	return &b, nil
}

func (s *Service) ListBookings(ctx context.Context, facilityID int64, date, status string, year, month int) ([]Booking, error) {
	q := `
SELECT b.id, b.facility_id, b.slot_id, b.user_id, COALESCE(b.customer_name,''), COALESCE(b.customer_phone,''),
       COALESCE(b.customer_email,''), b.status, COALESCE(b.notes,''),
       COALESCE(b.payment_status,'unpaid'), COALESCE(b.pay_mode,''), b.amount_paid,
       s.slot_date::text, s.start_time::text, s.end_time::text, c.name, b.created_at::text
FROM booking_bookings b
JOIN booking_slots s ON s.id = b.slot_id
JOIN booking_courts c ON c.id = s.court_id
WHERE b.facility_id=$1`
	args := []interface{}{facilityID}
	n := 2
	if date != "" {
		if err := validateDate(date); err != nil {
			return nil, err
		}
		q += fmt.Sprintf(` AND s.slot_date=$%d::date`, n)
		args = append(args, date)
		n++
	}
	if status != "" {
		status = strings.ToLower(strings.TrimSpace(status))
		if status != "confirmed" && status != "cancelled" {
			return nil, fmt.Errorf("invalid_status")
		}
		q += fmt.Sprintf(` AND b.status=$%d`, n)
		args = append(args, status)
		n++
	}
	if year > 0 && month >= 1 && month <= 12 {
		q += fmt.Sprintf(` AND EXTRACT(YEAR FROM s.slot_date)=$%d AND EXTRACT(MONTH FROM s.slot_date)=$%d`, n, n+1)
		args = append(args, year, month)
	}
	q += ` ORDER BY s.slot_date DESC, s.start_time DESC LIMIT 500`

	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Booking{}
	for rows.Next() {
		var b Booking
		var userID sql.NullInt64
		var amount sql.NullFloat64
		if err := rows.Scan(
			&b.ID, &b.FacilityID, &b.SlotID, &userID, &b.CustomerName, &b.CustomerPhone,
			&b.CustomerEmail, &b.Status, &b.Notes, &b.PaymentStatus, &b.PayMode, &amount,
			&b.SlotDate, &b.StartTime, &b.EndTime, &b.CourtName, &b.CreatedAt,
		); err != nil {
			return nil, err
		}
		if userID.Valid {
			v := userID.Int64
			b.UserID = &v
		}
		if amount.Valid {
			v := amount.Float64
			b.AmountPaid = &v
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func validateDate(date string) error {
	date = strings.TrimSpace(date)
	if date == "" {
		return fmt.Errorf("date_required")
	}
	_, err := time.Parse("2006-01-02", date)
	if err != nil {
		return fmt.Errorf("invalid_date")
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanSlotRows(row rowScanner) ([]Slot, error) {
	var s Slot
	if err := row.Scan(
		&s.ID, &s.FacilityID, &s.CourtID, &s.CourtName, &s.SlotDate, &s.StartTime, &s.EndTime, &s.Status, &s.Notes,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("slot_not_found")
		}
		return nil, err
	}
	s.StartTime = trimTime(s.StartTime)
	s.EndTime = trimTime(s.EndTime)
	return []Slot{s}, nil
}

func scanSlots(rows *sql.Rows) ([]Slot, error) {
	out := []Slot{}
	for rows.Next() {
		var s Slot
		if err := rows.Scan(
			&s.ID, &s.FacilityID, &s.CourtID, &s.CourtName, &s.SlotDate, &s.StartTime, &s.EndTime, &s.Status, &s.Notes,
			&s.CustomerName, &s.CustomerPhone,
		); err != nil {
			return nil, err
		}
		s.StartTime = trimTime(s.StartTime)
		s.EndTime = trimTime(s.EndTime)
		out = append(out, s)
	}
	return out, rows.Err()
}

func trimTime(t string) string {
	// pg may return HH:MM:SS or HH:MM:SS.ffffff
	if len(t) >= 8 {
		return t[:8]
	}
	return t
}
