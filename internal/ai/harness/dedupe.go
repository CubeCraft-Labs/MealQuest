package harness

import (
	"strings"
	"unicode"
)

// SimilarThreshold is the token-set similarity at or above which two dinner
// names are the same dinner.
const SimilarThreshold = 0.85

// fillerWords carry no meaning in a dinner name and are dropped by
// Normalize.
var fillerWords = map[string]bool{
	"easy": true, "best": true, "homemade": true, "quick": true, "simple": true,
	"the": true, "a": true, "recipe": true, "classic": true,
}

// accentFolds maps each lowercase accented Latin letter to its plain form.
var accentFolds = func() map[rune]string {
	m := map[rune]string{'ß': "ss", 'æ': "ae", 'œ': "oe", 'þ': "th", 'ð': "d"}
	for plain, accented := range map[string]string{
		"a": "àáâãäåāăą", "c": "çćĉċč", "d": "ďđ", "e": "èéêëēĕėęě",
		"g": "ĝğġģ", "h": "ĥħ", "i": "ìíîïĩīĭįı", "j": "ĵ", "k": "ķ",
		"l": "ĺļľŀł", "n": "ñńņňŉ", "o": "òóôõöøōŏő", "r": "ŕŗř",
		"s": "śŝşšș", "t": "ţťŧț", "u": "ùúûüũūŭůűų", "w": "ŵ", "y": "ýÿŷ",
		"z": "źżž",
	} {
		for _, r := range accented {
			m[r] = plain
		}
	}
	return m
}()

// Normalize returns the comparable form of a dinner name: lowercased,
// accents folded to plain letters, apostrophes removed, other punctuation
// turned into spaces, filler words (see fillerWords) dropped and whitespace
// collapsed to single spaces. "Easy Chicken Tikka-Masala!" becomes
// "chicken tikka masala".
func Normalize(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case unicode.Is(unicode.Mn, r), r == '\'', r == '’':
			// Combining accents and apostrophes join their neighbours.
		case accentFolds[r] != "":
			b.WriteString(accentFolds[r])
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	words := strings.Fields(b.String())
	kept := words[:0]
	for _, w := range words {
		if !fillerWords[w] {
			kept = append(kept, w)
		}
	}
	return strings.Join(kept, " ")
}

// Similar reports whether a and b name the same dinner: the token-set
// similarity of their normalized forms is at least SimilarThreshold. A name
// with no words left after normalizing is similar to nothing.
func Similar(a, b string) bool {
	return similarity(tokenSet(a), tokenSet(b)) >= SimilarThreshold
}

// tokenSet returns the distinct words of name's normalized form.
func tokenSet(name string) map[string]bool {
	set := map[string]bool{}
	for _, w := range strings.Fields(Normalize(name)) {
		set[w] = true
	}
	return set
}

// similarity is the Jaccard index of a and b: shared words over all words.
// It is 0 when either set is empty.
func similarity(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	shared := 0
	for w := range a {
		if b[w] {
			shared++
		}
	}
	return float64(shared) / float64(len(a)+len(b)-shared)
}

// DedupeSources are the dinner names a new suggestion must not repeat.
type DedupeSources struct {
	Regulars []string
	QuestLog []string
	// Pending holds the names of suggestions not yet tried or passed.
	Pending []string
	// Declined holds the names of passed suggestions.
	Declined []string
}

// Deduper answers whether a candidate dinner name repeats a known one.
type Deduper struct {
	known []map[string]bool
}

// NewDeduper returns a Deduper over every name in src.
func NewDeduper(src DedupeSources) *Deduper {
	d := &Deduper{}
	for _, names := range [][]string{src.Regulars, src.QuestLog, src.Pending, src.Declined} {
		for _, name := range names {
			d.known = append(d.known, tokenSet(name))
		}
	}
	return d
}

// IsDuplicate reports whether name is Similar to any known name.
func (d *Deduper) IsDuplicate(name string) bool {
	candidate := tokenSet(name)
	for _, k := range d.known {
		if similarity(candidate, k) >= SimilarThreshold {
			return true
		}
	}
	return false
}
