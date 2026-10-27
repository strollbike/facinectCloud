package tournaments

import (
	"fmt"
	"strconv"
	"strings"
)

func RoundRobinMatchCount(n int) int {
	if n < 2 {
		return 0
	}
	return (n * (n - 1)) / 2
}

func SuggestPools(teamCount, preferredPerPool int) []int {
	if teamCount < 2 {
		if teamCount < 1 {
			return []int{}
		}
		return []int{teamCount}
	}
	preferred := preferredPerPool
	if preferred < 2 {
		preferred = 2
	}
	pools := int(float64(teamCount)/float64(preferred) + 0.5) // round
	if pools < 1 {
		pools = 1
	}
	for pools > 1 && (teamCount/pools) < 2 {
		pools--
	}
	base := teamCount / pools
	rem := teamCount % pools
	dist := make([]int, pools)
	for i := 0; i < pools; i++ {
		if i < rem {
			dist[i] = base + 1
		} else {
			dist[i] = base
		}
	}
	return dist
}

func ParsePoolDistribution(distribution string, expectedTotal int) ([]int, error) {
	parts := []int{}
	for _, p := range strings.Split(distribution, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid_pool_distribution")
		}
		parts = append(parts, n)
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("pool_distribution_required")
	}
	for _, size := range parts {
		if size < 2 {
			return nil, fmt.Errorf("pool_too_small")
		}
	}
	if expectedTotal > 0 {
		sum := 0
		for _, n := range parts {
			sum += n
		}
		if sum != expectedTotal {
			return nil, fmt.Errorf("pool_distribution_sum_mismatch")
		}
	}
	return parts, nil
}

// RRPairsWithinPool returns all-play-all pairs, optionally capped.
func RRPairsWithinPool(names []string, maxMatches int) [][2]string {
	pairs := [][2]string{}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			pairs = append(pairs, [2]string{names[i], names[j]})
			if maxMatches > 0 && len(pairs) >= maxMatches {
				return pairs
			}
		}
	}
	return pairs
}
