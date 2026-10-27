package tournaments

import (
	"math/rand"
	"sort"
	"strings"
	"time"
)

// PairingSide is an ordered competitor for fixture generation.
type PairingSide struct {
	Name    string
	Academy string
	Seed    int
}

func OrderSides(sides []PairingSide, mode string) ([]PairingSide, string) {
	mode = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(mode), " ", "_"), "-", "_"))
	switch mode {
	case "seeded":
		return seededOrder(sides), ""
	case "random":
		out := append([]PairingSide{}, sides...)
		rng := rand.New(rand.NewSource(time.Now().UnixNano()))
		rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		return out, ""
	case "manual":
		return append([]PairingSide{}, sides...), ""
	case "academy_separated", "":
		return academySeparated(sides)
	default:
		return academySeparated(sides)
	}
}

func OrderNames(names []string, mode string) []string {
	sides := make([]PairingSide, 0, len(names))
	for i, n := range names {
		sides = append(sides, PairingSide{Name: n, Seed: i + 1})
	}
	ordered, _ := OrderSides(sides, mode)
	out := make([]string, 0, len(ordered))
	for _, s := range ordered {
		out = append(out, s.Name)
	}
	return out
}

func StandardSeedSlots(bracketSize int) []int {
	if bracketSize <= 1 {
		return []int{0}
	}
	seeds := []int{1, 2}
	for size := 2; size < bracketSize; size *= 2 {
		next := make([]int, 0, size*2)
		sum := size*2 + 1
		for _, s := range seeds {
			next = append(next, s, sum-s)
		}
		seeds = next
	}
	slotBySeed := map[int]int{}
	for idx, seedNum := range seeds {
		slotBySeed[seedNum] = idx
	}
	out := make([]int, 0, bracketSize)
	for s := 1; s <= bracketSize; s++ {
		if idx, ok := slotBySeed[s]; ok {
			out = append(out, idx)
		}
	}
	return out
}

func seededOrder(sides []PairingSide) []PairingSide {
	out := append([]PairingSide{}, sides...)
	sort.SliceStable(out, func(i, j int) bool {
		sa, sb := out[i].Seed, out[j].Seed
		if sa == 0 {
			sa = 1 << 30
		}
		if sb == 0 {
			sb = 1 << 30
		}
		if sa == sb {
			return out[i].Name < out[j].Name
		}
		return sa < sb
	})
	n := len(out)
	if n <= 2 {
		return out
	}
	bracket := NextPowerOfTwo(n)
	slots := make([]*PairingSide, bracket)
	positions := StandardSeedSlots(bracket)
	for i := range out {
		if i >= len(positions) {
			break
		}
		s := out[i]
		slots[positions[i]] = &s
	}
	ordered := make([]PairingSide, 0, n)
	for _, s := range slots {
		if s != nil {
			ordered = append(ordered, *s)
		}
	}
	return ordered
}

func academySeparated(sides []PairingSide) ([]PairingSide, string) {
	groups := map[string][]PairingSide{}
	keys := []string{}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	for _, s := range sides {
		ac := strings.TrimSpace(s.Academy)
		if ac == "" {
			ac = "Independent"
		}
		if _, ok := groups[ac]; !ok {
			keys = append(keys, ac)
		}
		groups[ac] = append(groups[ac], s)
	}
	for k := range groups {
		g := groups[k]
		rng.Shuffle(len(g), func(i, j int) { g[i], g[j] = g[j], g[i] })
		groups[k] = g
	}
	ordered := make([]PairingSide, 0, len(sides))
	for len(groups) > 0 {
		for _, k := range append([]string{}, keys...) {
			members, ok := groups[k]
			if !ok || len(members) == 0 {
				delete(groups, k)
				continue
			}
			ordered = append(ordered, members[0])
			members = members[1:]
			if len(members) == 0 {
				delete(groups, k)
			} else {
				groups[k] = members
			}
		}
	}
	warning := ""
	for i := 0; i+1 < len(ordered); i += 2 {
		a1 := strings.TrimSpace(ordered[i].Academy)
		a2 := strings.TrimSpace(ordered[i+1].Academy)
		if a1 == "" {
			a1 = "Independent"
		}
		if a2 == "" {
			a2 = "Independent"
		}
		if a1 != "Independent" && a1 == a2 {
			warning = "Could not fully separate all academy pairings in Round 1."
			break
		}
	}
	return ordered, warning
}
