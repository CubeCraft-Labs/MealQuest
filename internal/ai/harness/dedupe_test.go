package harness

import (
	"testing"
	"unicode"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name, want string
	}{
		{"Easy Chicken Tikka Masala", "chicken tikka masala"},
		{"  The   BEST homemade\tLasagna  ", "lasagna"},
		{"A Quick, Simple Classic Chili Recipe", "chili"},
		{"Tacos (beef)", "tacos beef"},
		{"Mac-and-Cheese!", "mac and cheese"},
		{"Shepherd's Pie", "shepherds pie"},
		{"Shepherd’s Pie", "shepherds pie"},
		{"Crème Brûlée", "creme brulee"},
		{"Jalapeño Poppers", "jalapeno poppers"},
		{"Paella à la Valenciana", "paella la valenciana"}, // "à" folds to the filler "a"
		{"Smørrebrød", "smorrebrod"},
		{"Bœuf Bourguignon", "boeuf bourguignon"},
		{"Käsespätzle mit Weißwurst", "kasespatzle mit weisswurst"},
		{"Cre\u0300me bru\u0302le\u0301e", "creme brulee"}, // decomposed accents
		{"İskender Kebab", "iskender kebab"},
		{"Pho 24", "pho 24"},
		{"Phở Bò", "pho bo"},
		{"Bún Chả Hà Nội", "bun cha ha noi"},
		{"Łosoś z Koperkiem", "losos z koperkiem"},
		{"Ćevapčići", "cevapcici"},
		{"Easy Recipe", ""},
		{"", ""},
		{"!?-- & ...", ""}, // punctuation only
	}
	for _, tt := range tests {
		if got := Normalize(tt.name); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// TestNormalizeCanonicalEquivalence checks that a composed spelling and its
// canonically equivalent decomposed spelling normalize equal and are Similar.
func TestNormalizeCanonicalEquivalence(t *testing.T) {
	tests := []struct {
		composed, decomposed, want string
	}{
		{"Phở", "Pho\u031b\u0309", "pho"}, // o + horn + hook above
		{"Bánh Xèo", "Ba\u0301nh Xe\u0300o", "banh xeo"},
		{"Cơm Tấm", "Co\u031bm Ta\u0302\u0301m", "com tam"},
		{"Ẓaʿtar Ȟash", "Z\u0323aʿtar H\u030cash", "zaʿtar hash"},
		{"Smǫrrebrød", "Smo\u0328rrebrød", "smorrebrod"},
		{"Smǿrrebrǿd", "Smø\u0301rrebrø\u0301d", "smorrebrod"}, // ǿ folds through ø
		{"Ǽbleskiver", "Æ\u0301bleskiver", "aebleskiver"},      // ǽ folds through æ
	}
	for _, tt := range tests {
		c, d := Normalize(tt.composed), Normalize(tt.decomposed)
		if c != tt.want || d != tt.want {
			t.Errorf("Normalize(%q) = %q, Normalize(%+q) = %q, want both %q",
				tt.composed, c, tt.decomposed, d, tt.want)
		}
		if !Similar(tt.composed, tt.decomposed) {
			t.Errorf("Similar(%q, %+q) = false, want true", tt.composed, tt.decomposed)
		}
		dedupe := NewDeduper(DedupeSources{Regulars: []string{tt.decomposed}})
		if !dedupe.IsDuplicate(tt.composed) {
			t.Errorf("IsDuplicate(%q) over %+q = false, want true", tt.composed, tt.decomposed)
		}
	}
}

// TestLetterFoldsFullyReduced checks that every fold result is final:
// Normalize writes a result without folding it again, so a result that
// still holds a foldable letter, a combining mark or an upper-case letter
// would split canonically equivalent spellings (ǿ -> ø instead of o).
func TestLetterFoldsFullyReduced(t *testing.T) {
	for r, folded := range letterFolds {
		for _, c := range folded {
			if letterFolds[c] != "" || unicode.Is(unicode.Mn, c) || unicode.ToLower(c) != c {
				t.Errorf("letterFolds[%U] = %q holds %U, which Normalize would change", r, folded, c)
			}
		}
	}
}

func TestSimilar(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"Easy Chicken Tikka Masala", "chicken tikka masala", true},
		{"Beef Tacos", "Tacos (beef)", true},
		{"Chicken tacos", "Fish tacos", false},
		{"Spaghetti Bolognese", "spaghetti bolognese recipe", true},
		{"Crème Brûlée", "creme brulee", true},
		{"Jalapeño Poppers", "JALAPENO POPPERS", true},
		{"Smørrebrød", "Smorrebrod", true},
		{"Phở", "Pho\u031b\u0309", true},
		{"Phở", "Pho", true},
		{"Chicken Tikka Masala", "Chicken Tikka Masala Curry", false}, // 3/4
		{"Tacos", "Fish tacos", false},
		{"Pad Thai", "Thai Green Curry", false},
		{"Easy Recipe", "The Classic", false}, // nothing left to compare
		{"Recipe", "Chili", false},
	}
	for _, tt := range tests {
		if got := Similar(tt.a, tt.b); got != tt.want {
			t.Errorf("Similar(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
		if got := Similar(tt.b, tt.a); got != tt.want {
			t.Errorf("Similar(%q, %q) = %v, want %v", tt.b, tt.a, got, tt.want)
		}
	}
}

func TestSimilarity(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want float64
	}{
		{"Easy Chicken Curry", "chicken curry", 1},
		{"Thai Green Curry", "Chicken curry", 0.25},
		{"Beef Stew", "Chicken curry", 0},
		{"The Recipe", "chicken curry", 0},
	} {
		if got := Similarity(tc.a, tc.b); got != tc.want {
			t.Errorf("Similarity(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestDeduper(t *testing.T) {
	d := NewDeduper(DedupeSources{
		Regulars: []string{"Beef Tacos", "Spaghetti Bolognese"},
		QuestLog: []string{"Chicken Tikka Masala"},
		Pending:  []string{"Crème Brûlée"},
		Declined: []string{"Shepherd's Pie"},
	})
	tests := []struct {
		name string
		want bool
	}{
		{"Tacos (beef)", true},                  // Regular
		{"spaghetti bolognese recipe", true},    // Regular
		{"Easy Chicken Tikka Masala", true},     // Quest log
		{"creme brulee", true},                  // pending suggestion
		{"The Best Shepherds Pie", true},        // declined
		{"Fish tacos", false},                   // shares only "tacos"
		{"Chicken Tikka Masala Curry", false},   // below the threshold
		{"Jalapeño Poppers", false},             // new
		{"Easy Recipe", false},                  // nothing left to compare
		{"Spaghetti Carbonara", false},          // shares only "spaghetti"
		{"Bolognese Spaghetti (Classic)", true}, // word order does not matter
	}
	for _, tt := range tests {
		if got := d.IsDuplicate(tt.name); got != tt.want {
			t.Errorf("IsDuplicate(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestDeduperEmpty(t *testing.T) {
	if NewDeduper(DedupeSources{}).IsDuplicate("Beef Tacos") {
		t.Error("an empty Deduper reported a duplicate")
	}
}
