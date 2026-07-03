package api

// Fuzzy anchor recovery (Phase 3). When an upstream sync / merge /
// revision rewrites the text a comment was anchored to, the exact-
// substring check fails and the comment orphans — even when a human
// can see the "same" sentence sitting right there with a word
// changed. This matcher finds the best approximately-matching
// substring so the comment stays attached.
//
// Deliberately conservative: a wrong re-anchor is worse than an
// orphan (the comment silently points at the wrong text), so we
// require BOTH a high absolute similarity AND a clear margin over
// the runner-up match. Ambiguous cases stay orphans.
//
// Suggestions are exempt on purpose: apply-suggestion keeps its
// strict exact-match requirement — a fuzzy match is fine for
// DISPLAYING a comment, not for MUTATING the doc based on it.

import (
	"sort"
	"strings"
	"unicode"
)

const (
	// Anchors shorter than this are too ambiguous to fuzzy-match —
	// "the API" appears everywhere.
	fuzzyMinAnchorRunes = 12
	// Very large docs get O(len(plain)/step × window) scoring passes;
	// past this size we skip and log rather than burn CPU.
	fuzzyMaxPlainBytes = 512 << 10
	// Absolute floor for the winning window's trigram Dice score.
	fuzzyThreshold = 0.75
	// The winner must beat the best NON-OVERLAPPING candidate by this
	// much, or we refuse — two similar paragraphs must not steal each
	// other's comments.
	fuzzyMargin = 0.08
)

// fuzzyFindAnchor locates the best approximate match for `exact`
// inside `plain` (the rendered plain text of the new content).
// Returns the matched substring — expanded to word boundaries, so it
// stays a verbatim substring of plain — and whether the match met the
// confidence bar.
func fuzzyFindAnchor(plain, exact string) (string, bool) {
	if len(plain) > fuzzyMaxPlainBytes {
		return "", false
	}
	needle := []rune(strings.TrimSpace(exact))
	if len(needle) < fuzzyMinAnchorRunes {
		return "", false
	}
	pr := []rune(plain)
	if len(pr) < fuzzyMinAnchorRunes {
		return "", false
	}

	lowNeedle := lowerRunes(needle)
	lowPlain := lowerRunes(pr)
	needleTri := runeTrigrams(lowNeedle)
	if len(needleTri) == 0 {
		return "", false
	}

	n := len(needle)
	step := n / 8
	if step < 4 {
		step = 4
	}
	// Window sizes bracket the original anchor length — rewrites
	// usually change length a little, not a lot.
	sizes := []int{n, n - n/5, n + n/4}

	type cand struct {
		start, end int
		score      float64
	}
	var cands []cand
	for _, w := range sizes {
		if w < fuzzyMinAnchorRunes || w > len(pr) {
			continue
		}
		for s := 0; s+w <= len(pr); s += step {
			sc := trigramDice(needleTri, lowPlain[s:s+w])
			// Keep everything within striking distance of the
			// threshold; the margin check below needs the runner-up.
			if sc >= fuzzyThreshold-fuzzyMargin {
				cands = append(cands, cand{s, s + w, sc})
			}
		}
	}
	if len(cands) == 0 {
		return "", false
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	best := cands[0]
	if best.score < fuzzyThreshold {
		return "", false
	}
	// Ambiguity guard: the best candidate that does NOT overlap the
	// winner. Overlapping candidates are just offset-shifted views of
	// the same passage — they don't count as competition.
	for _, c := range cands[1:] {
		if c.end <= best.start || c.start >= best.end {
			if best.score-c.score < fuzzyMargin {
				return "", false
			}
			break // cands is sorted; the first non-overlap is the strongest
		}
	}

	// Expand to word boundaries so the anchor doesn't slice a word in
	// half, then trim whitespace. The result stays a verbatim
	// substring of plain — that's what the frontend resolves against.
	s, e := best.start, best.end
	for s > 0 && !unicode.IsSpace(pr[s-1]) {
		s--
	}
	for e < len(pr) && !unicode.IsSpace(pr[e]) {
		e++
	}
	out := strings.TrimSpace(string(pr[s:e]))
	if out == "" {
		return "", false
	}
	return out, true
}

func lowerRunes(rs []rune) []rune {
	out := make([]rune, len(rs))
	for i, r := range rs {
		out[i] = unicode.ToLower(r)
	}
	return out
}

// runeTrigrams returns the set of rune-trigrams in rs.
func runeTrigrams(rs []rune) map[string]struct{} {
	out := make(map[string]struct{}, len(rs))
	for i := 0; i+3 <= len(rs); i++ {
		out[string(rs[i:i+3])] = struct{}{}
	}
	return out
}

// trigramDice computes the Sørensen–Dice coefficient between a
// precomputed trigram set and a window of runes.
func trigramDice(needle map[string]struct{}, window []rune) float64 {
	if len(window) < 3 {
		return 0
	}
	seen := make(map[string]struct{}, len(window))
	inter := 0
	for i := 0; i+3 <= len(window); i++ {
		t := string(window[i : i+3])
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		if _, ok := needle[t]; ok {
			inter++
		}
	}
	denom := len(needle) + len(seen)
	if denom == 0 {
		return 0
	}
	return 2 * float64(inter) / float64(denom)
}
