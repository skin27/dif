package engine

import (
	"slices"
	"strings"
)

// maxSuggestions is the number of flow ids a "not found" error suggests at most.
const maxSuggestions = 3

// suggest returns the ids that closely match id, best first: ids that differ
// only in case, start with id (at least 3 characters), or are a few edits
// away (1 for short ids, more for longer ones).
func suggest(id string, ids []string) []string {
	if id == "" {
		return nil
	}
	type match struct {
		id   string
		dist int
	}
	lower := strings.ToLower(id)
	limit := 1 + len(id)/5
	var matches []match
	for _, cand := range ids {
		c := strings.ToLower(cand)
		d := distance(lower, c)
		if d <= limit || (len(id) >= 3 && strings.HasPrefix(c, lower)) {
			matches = append(matches, match{cand, d})
		}
	}
	slices.SortFunc(matches, func(a, b match) int {
		if a.dist != b.dist {
			return a.dist - b.dist
		}
		return strings.Compare(a.id, b.id)
	})

	var out []string
	for _, m := range matches[:min(len(matches), maxSuggestions)] {
		out = append(out, m.id)
	}
	return out
}

// distance is the Levenshtein edit distance between a and b, in bytes.
func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
