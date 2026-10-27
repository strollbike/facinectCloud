package tournaments

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// ToggleEntries flips tournaments.entries_open (PHP toggle_entries).
func (s *Service) ToggleEntries(ctx context.Context, tournamentID int64) (int, error) {
	if tournamentID <= 0 {
		return 0, fmt.Errorf("invalid_tournament")
	}
	var open bool
	err := s.DB.QueryRowContext(ctx, `
UPDATE tournaments
SET entries_open = NOT entries_open, updated_at = NOW()
WHERE id = $1
RETURNING entries_open`, tournamentID).Scan(&open)
	if err != nil {
		return 0, fmt.Errorf("not_found")
	}
	if open {
		return 1, nil
	}
	return 0, nil
}

// MoveEntryCategory updates an entry's category (PHP move_category).
func (s *Service) MoveEntryCategory(ctx context.Context, tournamentID, entryID int64, newCategory string) (*Entry, error) {
	cat := strings.TrimSpace(newCategory)
	if tournamentID <= 0 || entryID <= 0 || cat == "" {
		return nil, fmt.Errorf("invalid_move")
	}
	cats, err := s.ListCategories(ctx, tournamentID)
	if err != nil {
		return nil, err
	}
	if len(cats) > 0 {
		ok := false
		for _, c := range cats {
			if strings.EqualFold(c.Name, cat) {
				cat = c.Name
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("invalid_category")
		}
	}
	res, err := s.DB.ExecContext(ctx, `
UPDATE tournament_entries SET category=$1
WHERE id=$2 AND tournament_id=$3`, cat, entryID, tournamentID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("not_found")
	}
	return s.getEntry(ctx, entryID)
}

// SpotPlayer is one player payload from StrollX spot_register.
type SpotPlayer struct {
	FirstName       string
	LastName        string
	PlayerName      string
	Phone           string
	Email           string
	Gender          string
	WhatsAppNumber  string
	Raw             map[string]interface{}
}

// SpotRegisterInput mirrors PHP spot_register body.
type SpotRegisterInput struct {
	CategoryName string
	Players      []SpotPlayer
}

// SpotRegisterResult is returned to StrollX.
type SpotRegisterResult struct {
	RegID         int64   `json:"reg_id"`
	EntryID       int64   `json:"entry_id"`
	RegCode       string  `json:"reg_code"`
	TotalFee      float64 `json:"total_fee"`
	PlatformFee   float64 `json:"platform_fee"`
	PaymentStatus string  `json:"payment_status"`
	WhatsAppSent  int     `json:"whatsapp_sent"`
	Entries       []Entry `json:"entries,omitempty"`
}

// SpotRegister creates pending entries for admin spot registration (PHP spot_register).
// Admin path: does not require allow_spot_payment or entries_open.
func (s *Service) SpotRegister(ctx context.Context, tournamentID int64, in SpotRegisterInput) (*SpotRegisterResult, error) {
	if tournamentID <= 0 {
		return nil, fmt.Errorf("invalid_tournament")
	}
	tour, err := s.Get(ctx, tournamentID)
	if err != nil {
		return nil, fmt.Errorf("not_found")
	}
	if strings.EqualFold(tour.Status, "cancelled") || strings.EqualFold(tour.Status, "completed") {
		return nil, fmt.Errorf("registration_closed")
	}
	cat := strings.TrimSpace(in.CategoryName)
	if cat == "" {
		return nil, fmt.Errorf("category_required")
	}
	fee := 0.0
	cats, err := s.ListCategories(ctx, tournamentID)
	if err != nil {
		return nil, err
	}
	okCat := false
	for _, c := range cats {
		if strings.EqualFold(c.Name, cat) {
			cat = c.Name
			fee = c.Fee
			okCat = true
			break
		}
	}
	if len(cats) > 0 && !okCat {
		return nil, fmt.Errorf("invalid_category")
	}
	if len(in.Players) == 0 {
		return nil, fmt.Errorf("players_required")
	}

	regCode := "SPOT-" + randomHex(4)
	txn := "SPOT_" + regCode
	created := make([]Entry, 0, len(in.Players))
	var captain *Entry

	for i, p := range in.Players {
		name := strings.TrimSpace(p.PlayerName)
		if name == "" {
			name = strings.TrimSpace(strings.TrimSpace(p.FirstName) + " " + strings.TrimSpace(p.LastName))
		}
		if name == "" {
			name = fmt.Sprintf("Player %d", i+1)
		}
		phone := strings.TrimSpace(p.Phone)
		if phone == "" {
			phone = strings.TrimSpace(p.WhatsAppNumber)
		}
		gender := normalizeSpotGender(p.Gender)
		entry, err := s.CreateEntry(ctx, tournamentID, EntryInput{
			Category:      cat,
			Gender:        gender,
			PlayerName:    name,
			Phone:         phone,
			Email:         strings.TrimSpace(p.Email),
			PaymentStatus: "pending",
			Status:        "active",
		})
		if err != nil {
			return nil, err
		}
		created = append(created, *entry)
		if captain == nil {
			captain = entry
		}
	}

	waSent := 0
	if captain != nil {
		// Spot confirmation WhatsApp (entry fee only — no platform fee), matching PHP.
		before := captain.Phone
		if digitsOnlyTourPhone(before) != "" {
			s.notifyTournamentRegistrationConfirmation(ctx, captain, fee, txn)
			waSent = 1
		}
		_ = before
	}

	primaryID := int64(0)
	if captain != nil {
		primaryID = captain.ID
	}
	return &SpotRegisterResult{
		RegID:         primaryID,
		EntryID:       primaryID,
		RegCode:       regCode,
		TotalFee:      fee,
		PlatformFee:   0,
		PaymentStatus: "Pending",
		WhatsAppSent:  waSent,
		Entries:       created,
	}, nil
}

func normalizeSpotGender(g string) string {
	gl := strings.ToLower(strings.TrimSpace(g))
	switch gl {
	case "male", "m", "boy", "boys", "man":
		return "Male"
	case "female", "f", "girl", "girls", "woman":
		return "Female"
	default:
		return strings.TrimSpace(g)
	}
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", nBytes)
	}
	return strings.ToUpper(hex.EncodeToString(b))
}

// SpotPlayerFromMap extracts fields from a StrollX player map.
func SpotPlayerFromMap(m map[string]interface{}) SpotPlayer {
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k]; ok && v != nil {
				s := strings.TrimSpace(fmt.Sprint(v))
				if s != "" && s != "<nil>" {
					return s
				}
			}
		}
		return ""
	}
	return SpotPlayer{
		FirstName:      str("first_name"),
		LastName:       str("last_name"),
		PlayerName:     str("player_name", "full_name", "name"),
		Phone:          str("phone", "mobile", "WhatsApp Number"),
		WhatsAppNumber: str("whatsapp_number", "whatsapp", "wa"),
		Email:          str("email"),
		Gender:         str("gender"),
		Raw:            m,
	}
}
