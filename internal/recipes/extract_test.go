package recipes

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readPage(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func intp(n int) *int { return &n }

func fmtMinutes(m *int) string {
	if m == nil {
		return "nil"
	}
	b, _ := json.Marshal(*m)
	return string(b)
}

func TestExtractRealPages(t *testing.T) {
	tests := []struct {
		file, url               string
		title, site, image, yld string
		ingredients, steps      int
		prep, cook, total       *int
		firstStep               string
	}{
		{
			// A Recipe script beside VideoObject and BreadcrumbList scripts;
			// image is an array of one sized ImageObject.
			file: "bbcgoodfood.html", url: "https://www.bbcgoodfood.com/recipes/easy-chicken-curry",
			title: "Easy chicken curry", site: "Good Food",
			image:       "https://images.immediate.co.uk/production/volatile/sites/30/2020/08/easy-chicken-curry-aa22a0b.jpg?resize=440,400",
			ingredients: 11, steps: 4, prep: intp(5), cook: intp(45), total: intp(50), yld: "4",
			firstStep: "Heat the oil in a flameproof casserole dish",
		},
		{
			// Yoast @graph; image is a list of bare URLs, the first the
			// full-size one; no publisher, so og:site_name names the site.
			file: "budgetbytes.html", url: "https://www.budgetbytes.com/one-pot-creamy-cajun-chicken-pasta/",
			title: "One Pot Creamy Cajun Chicken Pasta", site: "Budget Bytes",
			image:       "https://www.budgetbytes.com/wp-content/uploads/2018/10/One-Pot-Creamy-Cajun-Chicken-Pasta-pan.jpg",
			ingredients: 17, steps: 6, prep: intp(10), cook: intp(20), total: intp(30), yld: "4",
			firstStep: "Combine the ingredients for the Cajun seasoning",
		},
		{
			// HowToSection groups (ragu, cheese sauce, assembly), flattened.
			file: "recipetineats.html", url: "https://www.recipetineats.com/lasagna/",
			title: "Lasagna", site: "RecipeTin Eats",
			image:       "https://www.recipetineats.com/tachyon/2017/05/Lasagne-recipe-3-main-square.jpg",
			ingredients: 24, steps: 18, prep: intp(20), cook: intp(240), total: intp(260), yld: "10",
			firstStep: "Heat oil in a large heavy based pot",
		},
		{
			// @graph of one node; a single ImageObject; instructions as a
			// list of plain strings; hours and minutes.
			file: "kingarthur.html", url: "https://www.kingarthurbaking.com/recipes/classic-sandwich-bread-recipe",
			title: "Classic Sandwich Bread", site: "King Arthur Baking",
			image:       "https://www.kingarthurbaking.com/sites/default/files/2026-02/Classic-Sandwich-Bread-12.jpg",
			ingredients: 7, steps: 7, prep: intp(12), cook: intp(40), total: intp(162), yld: "16",
			firstStep: "Weigh your flour",
		},
		{
			// A top-level array; ImageObjects of different sizes.
			file: "delish.html", url: "https://www.delish.com/cooking/recipe-ideas/a19636089/creamy-tuscan-chicken-recipe/",
			title: "Creamy Tuscan Chicken", site: "Delish",
			image:       "https://hips.hearstapps.com/hmg-prod/images/1d09a24f-5b76-49f4-9ad6-496acdc03933.jpg?crop=0.502xw:1.00xh;0.250xw,0&resize=1200:*",
			ingredients: 12, steps: 4, prep: intp(10), cook: intp(0), total: intp(45), yld: "4 serving(s)",
			firstStep: "In a large skillet over medium heat, heat oil.",
		},
		{
			// A bare Recipe object; image is one URL; HTML entities in steps.
			file: "foodcom.html", url: "https://www.food.com/recipe/best-banana-bread-2886",
			title: "Best Banana Bread", site: "Food.com",
			image:       "https://img.sndimg.com/food/image/upload/q_92,fl_progressive,w_1200,c_scale/v1/img/recipes/28/86/wBZxNua1T8yfDwbfo9Fz_0S9A9315.jpg",
			ingredients: 8, steps: 10, prep: intp(10), cook: intp(60), total: intp(70), yld: "1 loaf, 10 serving(s)",
			firstStep: "Remove odd pots and pans from oven.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			r, err := Extract(readPage(t, tt.file), tt.url)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if r.Title != tt.title {
				t.Errorf("Title = %q, want %q", r.Title, tt.title)
			}
			if r.SiteName != tt.site {
				t.Errorf("SiteName = %q, want %q", r.SiteName, tt.site)
			}
			if r.ImageURL != tt.image {
				t.Errorf("ImageURL = %q, want %q", r.ImageURL, tt.image)
			}
			if len(r.Ingredients) != tt.ingredients {
				t.Errorf("%d ingredients, want %d", len(r.Ingredients), tt.ingredients)
			}
			if len(r.Steps) != tt.steps {
				t.Errorf("%d steps, want %d", len(r.Steps), tt.steps)
			}
			if len(r.Steps) > 0 && !strings.HasPrefix(r.Steps[0], tt.firstStep) {
				t.Errorf("first step = %q, want prefix %q", r.Steps[0], tt.firstStep)
			}
			for name, got := range map[string][2]*int{
				"prep": {r.PrepMinutes, tt.prep}, "cook": {r.CookMinutes, tt.cook}, "total": {r.TotalMinutes, tt.total},
			} {
				if fmtMinutes(got[0]) != fmtMinutes(got[1]) {
					t.Errorf("%s minutes = %s, want %s", name, fmtMinutes(got[0]), fmtMinutes(got[1]))
				}
			}
			if r.Yield != tt.yld {
				t.Errorf("Yield = %q, want %q", r.Yield, tt.yld)
			}
			var node map[string]any
			if err := json.Unmarshal(r.JSONLD, &node); err != nil || node["name"] == nil {
				t.Errorf("JSONLD is not the Recipe node: %v", err)
			}
		})
	}
}

func TestExtractHowToSectionOrder(t *testing.T) {
	r, err := Extract(readPage(t, "recipetineats.html"), "https://www.recipetineats.com/lasagna/")
	if err != nil {
		t.Fatal(err)
	}
	// Ragu (5 steps), Cheese Sauce (5), Assemble (8): each section's first
	// step lands right after the previous section's last.
	for i, prefix := range map[int]string{
		0:  "Heat oil in a large heavy based pot",
		4:  "The ragu is ready when the meat",
		5:  "Warm milk up in a saucepan",
		10: "Preheat oven to 180°C/350°F.",
		17: "Stand for 5 to 10 minutes",
	} {
		if !strings.HasPrefix(r.Steps[i], prefix) {
			t.Errorf("step %d = %q, want prefix %q", i+1, r.Steps[i], prefix)
		}
	}
}

func TestExtractNoRecipe(t *testing.T) {
	_, err := Extract(readPage(t, "budgetbytes-about.html"), "https://www.budgetbytes.com/about/")
	if !errors.Is(err, ErrNoRecipe) {
		t.Fatalf("err = %v, want ErrNoRecipe", err)
	}
	_, err = Extract([]byte("<html><head><title>x</title></head></html>"), "https://example.com/")
	if !errors.Is(err, ErrNoRecipe) {
		t.Fatalf("page without JSON-LD: err = %v, want ErrNoRecipe", err)
	}
}

func TestExtractRefusesNonISODuration(t *testing.T) {
	// Bon Appétit writes cookTime as "20 minutes".
	_, err := Extract(readPage(t, "bonappetit.html"), "https://www.bonappetit.com/recipe/bas-best-chocolate-chip-cookies")
	if !errors.Is(err, ErrMalformedRecipe) || !strings.Contains(err.Error(), "cookTime") {
		t.Fatalf("err = %v, want ErrMalformedRecipe naming cookTime", err)
	}
}

// page wraps JSON-LD scripts in a minimal HTML page.
func page(head string, jsonLD ...string) []byte {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html><head>" + head)
	for _, s := range jsonLD {
		b.WriteString(`<script type="application/ld+json">` + s + "</script>")
	}
	b.WriteString("</head><body></body></html>")
	return []byte(b.String())
}

func TestExtractShapes(t *testing.T) {
	const pageURL = "https://www.example.com/recipes/stew/"
	tests := []struct {
		name  string
		html  []byte
		check func(t *testing.T, r Recipe)
	}{
		{
			name: "type array and relative image",
			html: page("", `{"@type":["Recipe","NewsArticle"],"name":"Stew","image":"/img/stew.jpg"}`),
			check: func(t *testing.T, r Recipe) {
				if r.ImageURL != "https://www.example.com/img/stew.jpg" {
					t.Errorf("ImageURL = %q", r.ImageURL)
				}
			},
		},
		{
			name: "largest image wins regardless of order",
			html: page("", `{"@type":"Recipe","name":"Stew","image":[
				"https://cdn.example.com/plain.jpg",
				{"@type":"ImageObject","url":"small.jpg","width":300,"height":200},
				{"@type":"ImageObject","contentUrl":"../big.jpg","width":"1600","height":"900"},
				{"@type":"ImageObject","url":"mid.jpg","width":1200,"height":800}]}`),
			check: func(t *testing.T, r Recipe) {
				if r.ImageURL != "https://www.example.com/recipes/big.jpg" {
					t.Errorf("ImageURL = %q", r.ImageURL)
				}
			},
		},
		{
			name: "instructions as one string",
			html: page("", `{"@type":"Recipe","name":"Stew","recipeInstructions":"Brown the beef.\r\n\r\nAdd the stock &amp; simmer.\nServe."}`),
			check: func(t *testing.T, r Recipe) {
				want := []string{"Brown the beef.", "Add the stock & simmer.", "Serve."}
				if strings.Join(r.Steps, "|") != strings.Join(want, "|") {
					t.Errorf("Steps = %q, want %q", r.Steps, want)
				}
			},
		},
		{
			name: "sections and steps flattened in order",
			html: page("", `{"@type":"Recipe","name":"Stew","recipeInstructions":[
				{"@type":"HowToSection","name":"Beef","itemListElement":[
					{"@type":"HowToStep","text":"one"},{"@type":"HowToStep","text":"two"}]},
				{"@type":"HowToStep","text":"three"},
				{"@type":"HowToSection","name":"Finish","itemListElement":["four"]}]}`),
			check: func(t *testing.T, r Recipe) {
				if got := strings.Join(r.Steps, " "); got != "one two three four" {
					t.Errorf("Steps = %q", got)
				}
			},
		},
		{
			name: "total derived from prep and cook",
			html: page("", `{"@type":"Recipe","name":"Stew","prepTime":"PT15M","cookTime":"PT1H30M"}`),
			check: func(t *testing.T, r Recipe) {
				if fmtMinutes(r.TotalMinutes) != "105" {
					t.Errorf("TotalMinutes = %s, want 105", fmtMinutes(r.TotalMinutes))
				}
			},
		},
		{
			name: "total not derived from prep alone",
			html: page("", `{"@type":"Recipe","name":"Stew","prepTime":"PT15M"}`),
			check: func(t *testing.T, r Recipe) {
				if r.TotalMinutes != nil || r.CookMinutes != nil {
					t.Errorf("total %s, cook %s; want nil", fmtMinutes(r.TotalMinutes), fmtMinutes(r.CookMinutes))
				}
			},
		},
		{
			name: "site name from host without publisher or og:site_name",
			html: page("", `{"@type":"Recipe","name":"Stew"}`),
			check: func(t *testing.T, r Recipe) {
				if r.SiteName != "example.com" {
					t.Errorf("SiteName = %q", r.SiteName)
				}
			},
		},
		{
			name: "og:site_name with attributes in any order",
			html: page(`<meta content="Our &amp; Kitchen" property="og:site_name">`, `{"@type":"Recipe","name":"Stew","publisher":{"@id":"#org"}}`),
			check: func(t *testing.T, r Recipe) {
				if r.SiteName != "Our & Kitchen" {
					t.Errorf("SiteName = %q", r.SiteName)
				}
			},
		},
		{
			name: "numeric yield and recipe in @graph beside others",
			html: page("", `{"@context":"https://schema.org","@graph":[{"@type":"WebPage"},{"@type":"Recipe","name":"Stew","recipeYield":6}]}`),
			check: func(t *testing.T, r Recipe) {
				if r.Yield != "6" {
					t.Errorf("Yield = %q", r.Yield)
				}
			},
		},
		{
			name: "JSON-LD text inside comments and other scripts is ignored",
			html: []byte(`<html><head><!-- <script type="application/ld+json">{"@type":"Recipe","name":"Old"}</script> -->` +
				`<script>var s = '<script type="application/ld+json">';</script>` +
				`<SCRIPT TYPE='application/ld+json; charset=utf-8'>{"@type":"Recipe","name":"Stew"}</SCRIPT></head></html>`),
			check: func(t *testing.T, r Recipe) {
				if r.Title != "Stew" {
					t.Errorf("Title = %q", r.Title)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Extract(tt.html, pageURL)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			tt.check(t, r)
		})
	}
}

func TestExtractErrors(t *testing.T) {
	tests := []struct {
		name string
		html []byte
		url  string
		want error
	}{
		{"two recipes", page("", `{"@type":"Recipe","name":"A"}`, `{"@type":"Recipe","name":"B"}`), "https://example.com/", ErrManyRecipes},
		{"invalid JSON", page("", `{"@type":"Recipe",`), "https://example.com/", ErrMalformedRecipe},
		{"no name", page("", `{"@type":"Recipe"}`), "https://example.com/", ErrMalformedRecipe},
		{"bad step type", page("", `{"@type":"Recipe","name":"A","recipeInstructions":[{"@type":"Thing"}]}`), "https://example.com/", ErrMalformedRecipe},
		{"step without text", page("", `{"@type":"Recipe","name":"A","recipeInstructions":[{"@type":"HowToStep","name":"x"}]}`), "https://example.com/", ErrMalformedRecipe},
		{"bad image width", page("", `{"@type":"Recipe","name":"A","image":{"url":"a.jpg","width":"wide"}}`), "https://example.com/", ErrMalformedRecipe},
		{"non-text ingredient", page("", `{"@type":"Recipe","name":"A","recipeIngredient":[{"name":"salt"}]}`), "https://example.com/", ErrMalformedRecipe},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Extract(tt.html, tt.url)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	if _, err := Extract(page("", `{"@type":"Recipe","name":"A"}`), "/relative"); err == nil {
		t.Fatal("relative page URL accepted")
	}
}

func TestISOMinutes(t *testing.T) {
	good := map[string]int{
		"PT0S": 0, "PT45M": 45, "PT1H": 60, "PT2H42M": 162, "PT240M": 240, "P1D": 1440,
		"P1DT2H": 1560, "PT90S": 2, "PT1.5H": 90, "PT0,5H": 30, "P1W": 10080, " PT5M ": 5,
	}
	for in, want := range good {
		got, err := isoMinutes(in)
		if err != nil || got != want {
			t.Errorf("isoMinutes(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "P", "PT", "20 minutes", "PT5", "P1Y", "P1M", "PT1D", "P5H", "PT5M1H", "PT5M5M", "P1DT", "PTxM", "PT1..5M"} {
		if got, err := isoMinutes(in); err == nil {
			t.Errorf("isoMinutes(%q) = %d, want error", in, got)
		}
	}
}
