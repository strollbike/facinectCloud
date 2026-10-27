package tournaments

import "testing"

func TestCategoryDisplayName(t *testing.T) {
	if got := categoryDisplayName(CategoryInput{Name: " Open "}); got != "Open" {
		t.Fatalf("name=%q", got)
	}
	if got := categoryDisplayName(CategoryInput{CategoryName: "U18"}); got != "U18" {
		t.Fatalf("alias=%q", got)
	}
}

func TestCategoryFeePrefersFee(t *testing.T) {
	if got := categoryFee(CategoryInput{Fee: 500, EntryFee: 100}); got != 500 {
		t.Fatalf("fee=%v", got)
	}
	if got := categoryFee(CategoryInput{EntryFee: 100}); got != 100 {
		t.Fatalf("entry=%v", got)
	}
}

func TestCategoryTeamSize(t *testing.T) {
	n, label := categoryTeamSize(CategoryInput{TeamSize: 2})
	if n != 2 || label != "" {
		t.Fatalf("got %d %q", n, label)
	}
	n, label = categoryTeamSize(CategoryInput{TeamSize: 0, Size: "3"})
	if n != 3 || label != "3" {
		t.Fatalf("parsed size got %d %q", n, label)
	}
	n, _ = categoryTeamSize(CategoryInput{TeamSize: 0, Size: "Solo"})
	if n != 1 {
		t.Fatalf("non-numeric size should keep min 1, got %d", n)
	}
}

func TestBoolToInt(t *testing.T) {
	if boolToInt(true) != 1 || boolToInt(false) != 0 {
		t.Fatal("boolToInt")
	}
}
