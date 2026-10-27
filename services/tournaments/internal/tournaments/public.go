package tournaments

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func isPublicRegistrationStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "upcoming", "live":
		return true
	default:
		return false
	}
}

// GetPublic returns a published tournament for the open registration page.
func (s *Service) GetPublic(ctx context.Context, id int64) (*Tournament, error) {
	if id <= 0 {
		return nil, fmt.Errorf("invalid_tournament")
	}
	t, err := s.Get(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("not_found")
		}
		return nil, err
	}
	if !isPublicRegistrationStatus(t.Status) {
		return nil, fmt.Errorf("registration_closed")
	}
	if t.EntriesOpen == 0 {
		return nil, fmt.Errorf("registration_closed")
	}
	return t, nil
}

// GetPublicBySlug resolves a published tournament by slug (optional facility filter).
func (s *Service) GetPublicBySlug(ctx context.Context, slug string, facilityID int64) (*Tournament, error) {
	slug = slugify(slug)
	if slug == "" || slug == "tournament" {
		return nil, fmt.Errorf("invalid_slug")
	}
	q := `
SELECT id FROM tournaments
WHERE slug=$1 AND status IN ('upcoming','live') AND entries_open = TRUE`
	args := []interface{}{slug}
	if facilityID > 0 {
		q += ` AND facility_id=$2`
		args = append(args, facilityID)
	}
	q += ` ORDER BY id DESC LIMIT 1`
	var id int64
	if err := s.DB.QueryRowContext(ctx, q, args...).Scan(&id); err != nil {
		return nil, fmt.Errorf("not_found")
	}
	return s.GetPublic(ctx, id)
}

// CreatePublicEntry registers a player when the tournament is open.
func (s *Service) CreatePublicEntry(ctx context.Context, tournamentID int64, in EntryInput) (*Entry, error) {
	if _, err := s.GetPublic(ctx, tournamentID); err != nil {
		return nil, err
	}
	cats, err := s.ListCategories(ctx, tournamentID)
	if err != nil {
		return nil, err
	}
	cat := strings.TrimSpace(in.Category)
	if len(cats) > 0 {
		if cat == "" {
			return nil, fmt.Errorf("category_required")
		}
		okCat := false
		for _, c := range cats {
			if strings.EqualFold(c.Name, cat) {
				okCat = true
				in.Category = c.Name
				break
			}
		}
		if !okCat {
			return nil, fmt.Errorf("invalid_category")
		}
	} else if cat == "" {
		in.Category = "Open"
	}
	phone := strings.TrimSpace(in.Phone)
	if phone == "" {
		return nil, fmt.Errorf("phone_required")
	}
	if strings.TrimSpace(in.PaymentStatus) == "" {
		in.PaymentStatus = "pending"
	}
	return s.CreateEntry(ctx, tournamentID, in)
}
