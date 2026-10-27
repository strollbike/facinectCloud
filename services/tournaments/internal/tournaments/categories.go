package tournaments

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Category is returned to StrollX (name + category_name / fee + entry_fee aliases).
type Category struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	CategoryName string  `json:"category_name"`
	Fee          float64 `json:"fee"`
	EntryFee     float64 `json:"entry_fee"`
	TeamSize     int     `json:"team_size"`
	Size         string  `json:"size,omitempty"`
	SortOrder    int     `json:"sort_order"`
}

// CategoryInput is accepted on create/update.
type CategoryInput struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	CategoryName string  `json:"category_name"`
	Fee          float64 `json:"fee"`
	EntryFee     float64 `json:"entry_fee"`
	TeamSize     int     `json:"team_size"`
	Size         string  `json:"size"`
}

func categoryDisplayName(in CategoryInput) string {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = strings.TrimSpace(in.CategoryName)
	}
	return name
}

func categoryFee(in CategoryInput) float64 {
	if in.Fee > 0 {
		return in.Fee
	}
	if in.EntryFee > 0 {
		return in.EntryFee
	}
	return in.Fee
}

func categoryTeamSize(in CategoryInput) (int, string) {
	sizeLabel := strings.TrimSpace(in.Size)
	n := in.TeamSize
	if n < 1 {
		n = 1
	}
	if sizeLabel != "" {
		if parsed := parsePositiveInt(sizeLabel); parsed > 0 {
			n = parsed
		}
	}
	return n, sizeLabel
}

func parsePositiveInt(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func (s *Service) ListCategories(ctx context.Context, tournamentID int64) ([]Category, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, name, fee::float8, team_size, COALESCE(size_label,''), sort_order
FROM tournament_categories
WHERE tournament_id=$1
ORDER BY sort_order, id`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Category{}
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.Fee, &c.TeamSize, &c.Size, &c.SortOrder); err != nil {
			return nil, err
		}
		c.CategoryName = c.Name
		c.EntryFee = c.Fee
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) attachCategories(ctx context.Context, t *Tournament) error {
	if t == nil {
		return nil
	}
	cats, err := s.ListCategories(ctx, t.ID)
	if err != nil {
		return err
	}
	t.Categories = cats
	t.PaymentOptions = map[string]interface{}{
		"allow_full_payment":    boolToInt(t.AllowFullPayment),
		"allow_advance_payment": boolToInt(t.AllowAdvancePayment),
		"allow_spot_payment":    boolToInt(t.AllowSpotPayment),
		"advance_amount":        t.AdvanceAmount,
	}
	return nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// replaceCategories upserts by id, renames entry/fixture refs, blocks delete-in-use.
// in == nil means leave categories unchanged. Empty slice clears unused categories.
func (s *Service) replaceCategories(ctx context.Context, tournamentID int64, in []CategoryInput) error {
	if in == nil {
		return nil
	}
	existing, err := s.ListCategories(ctx, tournamentID)
	if err != nil {
		return err
	}
	byID := map[int64]Category{}
	for _, c := range existing {
		byID[c.ID] = c
	}

	seen := map[int64]bool{}
	seenNames := map[string]bool{}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for i, raw := range in {
		name := categoryDisplayName(raw)
		if name == "" {
			continue
		}
		if len(name) > 64 {
			return fmt.Errorf("category_name_too_long")
		}
		key := strings.ToLower(name)
		if seenNames[key] {
			return fmt.Errorf("duplicate_category")
		}
		seenNames[key] = true
		fee := categoryFee(raw)
		teamSize, sizeLabel := categoryTeamSize(raw)
		sortOrder := i

		if raw.ID > 0 {
			old, ok := byID[raw.ID]
			if !ok || old.ID != raw.ID {
				return fmt.Errorf("category_not_found")
			}
			seen[raw.ID] = true
			if !strings.EqualFold(old.Name, name) {
				if err := renameCategoryRefsTx(ctx, tx, tournamentID, old.Name, name); err != nil {
					return err
				}
			}
			_, err = tx.ExecContext(ctx, `
UPDATE tournament_categories
SET name=$2, fee=$3, team_size=$4, size_label=$5, sort_order=$6, updated_at=NOW()
WHERE id=$1 AND tournament_id=$7`,
				raw.ID, name, fee, teamSize, sizeLabel, sortOrder, tournamentID)
			if err != nil {
				if strings.Contains(strings.ToLower(err.Error()), "tournament_categories") {
					return fmt.Errorf("duplicate_category")
				}
				return err
			}
			continue
		}

		var newID int64
		err = tx.QueryRowContext(ctx, `
INSERT INTO tournament_categories
  (tournament_id, name, fee, team_size, size_label, sort_order)
VALUES ($1,$2,$3,$4,$5,$6)
RETURNING id`,
			tournamentID, name, fee, teamSize, sizeLabel, sortOrder,
		).Scan(&newID)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "tournament_categories") {
				return fmt.Errorf("duplicate_category")
			}
			return err
		}
		seen[newID] = true
	}

	for _, old := range existing {
		if seen[old.ID] {
			continue
		}
		inUse, err := categoryInUseTx(ctx, tx, tournamentID, old.Name)
		if err != nil {
			return err
		}
		if inUse {
			return fmt.Errorf("category_in_use")
		}
		if _, err := tx.ExecContext(ctx, `
DELETE FROM tournament_categories WHERE id=$1 AND tournament_id=$2`,
			old.ID, tournamentID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func renameCategoryRefsTx(ctx context.Context, tx *sql.Tx, tournamentID int64, oldName, newName string) error {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if oldName == "" || newName == "" || strings.EqualFold(oldName, newName) {
		return nil
	}
	stmts := []string{
		`UPDATE tournament_entries SET category=$3 WHERE tournament_id=$1 AND category=$2`,
		`UPDATE tournament_fixtures SET category=$3 WHERE tournament_id=$1 AND category=$2`,
		`UPDATE tournament_score_events SET category=$3 WHERE tournament_id=$1 AND category=$2`,
		`UPDATE tournament_referees SET category=$3 WHERE tournament_id=$1 AND category=$2`,
		`UPDATE tournament_ref_tokens SET category=$3 WHERE tournament_id=$1 AND category=$2`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q, tournamentID, oldName, newName); err != nil {
			return err
		}
	}
	return nil
}

func categoryInUseTx(ctx context.Context, tx *sql.Tx, tournamentID int64, name string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, nil
	}
	var n int
	err := tx.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM tournament_entries WHERE tournament_id=$1 AND category=$2) +
  (SELECT COUNT(*) FROM tournament_fixtures WHERE tournament_id=$1 AND category=$2)
`, tournamentID, name).Scan(&n)
	return n > 0, err
}
