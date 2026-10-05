package recipes

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"net/url"
	"strconv"
	"strings"
)

var (
	// ErrNoRecipe is returned for a page with no schema.org Recipe node in
	// its JSON-LD.
	ErrNoRecipe = errors.New("recipes: page has no schema.org Recipe")
	// ErrManyRecipes is returned for a page with more than one Recipe node:
	// which one the page is about is not knowable.
	ErrManyRecipes = errors.New("recipes: page has more than one schema.org Recipe")
	// ErrMalformedRecipe is returned when the page's JSON-LD, or a field of
	// its Recipe node, is not in a shape schema.org allows.
	ErrMalformedRecipe = errors.New("recipes: malformed schema.org Recipe")
)

// Recipe is the schema.org Recipe extracted from a recipe page.
type Recipe struct {
	// Title is the recipe's name.
	Title string
	// SiteName is the publisher's name, else the page's og:site_name, else
	// its host without a leading "www.".
	SiteName string
	// ImageURL is the largest image, absolute; empty when the recipe has
	// none.
	ImageURL    string
	Ingredients []string
	// Steps are the instructions in order, HowToSection groups flattened.
	Steps []string
	// PrepMinutes, CookMinutes and TotalMinutes are nil when not given.
	// TotalMinutes is prep plus cook when the page gives both but no total.
	PrepMinutes  *int
	CookMinutes  *int
	TotalMinutes *int
	// Yield is recipeYield as text; empty when not given.
	Yield string
	// JSONLD is the Recipe node as found.
	JSONLD json.RawMessage
}

// Extract finds the schema.org Recipe in a page's application/ld+json
// scripts. Recipe nodes are found at the top level of a script, in a
// top-level array, or in an @graph. An image or publisher given as an
// {"@id": ...} reference resolves to the node with that @id found in the
// same places, in any of the page's scripts. pageURL is the page's own
// absolute URL; relative image URLs resolve against it.
func Extract(page []byte, pageURL string) (Recipe, error) {
	base, err := url.Parse(pageURL)
	if err != nil || !base.IsAbs() || base.Host == "" {
		return Recipe{}, fmt.Errorf("recipes: page URL %q is not absolute", pageURL)
	}
	doc := scanHTML(string(page))
	g := graph{ids: map[string][]map[string]any{}}
	for i, script := range doc.jsonLD {
		v, err := decodeJSON(script)
		if err != nil {
			return Recipe{}, fmt.Errorf("%w: JSON-LD script %d: %v", ErrMalformedRecipe, i+1, err)
		}
		g.collect(v)
	}
	nodes := g.recipes
	switch len(nodes) {
	case 0:
		return Recipe{}, ErrNoRecipe
	case 1:
	default:
		return Recipe{}, fmt.Errorf("%w: found %d", ErrManyRecipes, len(nodes))
	}
	r, err := recipeFrom(nodes[0], g, base, doc.ogSiteName)
	if err != nil {
		return Recipe{}, fmt.Errorf("%w: %v", ErrMalformedRecipe, err)
	}
	return r, nil
}

func decodeJSON(s string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after JSON value")
	}
	return v, nil
}

// graph holds the page's JSON-LD nodes: the Recipe nodes, and every node
// with an @id by that @id.
type graph struct {
	recipes []map[string]any
	ids     map[string][]map[string]any
}

// collect gathers the nodes in v: v itself, the elements of an array, and
// the members of an @graph. A bare {"@id": ...} reference is not a node.
func (g *graph) collect(v any) {
	switch n := v.(type) {
	case []any:
		for _, e := range n {
			g.collect(e)
		}
	case map[string]any:
		if hasType(n, "Recipe") {
			g.recipes = append(g.recipes, n)
		}
		if id, ok := n["@id"].(string); ok && len(n) > 1 {
			g.ids[id] = append(g.ids[id], n)
		}
		if members, ok := n["@graph"]; ok {
			g.collect(members)
		}
	}
}

// resolve returns the node a bare {"@id": ...} reference names, and v
// itself when v is not a reference. found is false for a reference to no
// node; a reference to more than one node is an error.
func (g graph) resolve(v any) (node any, found bool, err error) {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return v, true, nil
	}
	id, ok := m["@id"].(string)
	if !ok {
		return v, true, nil
	}
	switch nodes := g.ids[id]; len(nodes) {
	case 0:
		return v, false, nil
	case 1:
		return nodes[0], true, nil
	default:
		return nil, false, fmt.Errorf("@id %q names %d nodes", id, len(nodes))
	}
}

// hasType reports whether node's @type, a string or an array of strings,
// names the schema.org type typ, as a term or an absolute schema.org IRI.
func hasType(node map[string]any, typ string) bool {
	switch t := node["@type"].(type) {
	case string:
		return isSchemaType(t, typ)
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok && isSchemaType(s, typ) {
				return true
			}
		}
	}
	return false
}

func isSchemaType(s, typ string) bool {
	return s == typ || s == "https://schema.org/"+typ || s == "http://schema.org/"+typ
}

func recipeFrom(n map[string]any, g graph, base *url.URL, ogSiteName string) (Recipe, error) {
	var r Recipe
	var err error
	name, ok := n["name"].(string)
	if r.Title = cleanText(name); !ok || r.Title == "" {
		return Recipe{}, errors.New("name is missing")
	}
	if r.SiteName, err = siteName(n["publisher"], g, ogSiteName, base); err != nil {
		return Recipe{}, err
	}
	if r.ImageURL, err = imageURL(n["image"], g, base); err != nil {
		return Recipe{}, err
	}
	if r.Ingredients, err = ingredients(n["recipeIngredient"]); err != nil {
		return Recipe{}, err
	}
	if r.Steps, err = steps(n["recipeInstructions"]); err != nil {
		return Recipe{}, err
	}
	if r.PrepMinutes, err = minutes(n, "prepTime"); err != nil {
		return Recipe{}, err
	}
	if r.CookMinutes, err = minutes(n, "cookTime"); err != nil {
		return Recipe{}, err
	}
	if r.TotalMinutes, err = minutes(n, "totalTime"); err != nil {
		return Recipe{}, err
	}
	if r.TotalMinutes == nil && r.PrepMinutes != nil && r.CookMinutes != nil {
		total := *r.PrepMinutes + *r.CookMinutes
		r.TotalMinutes = &total
	}
	if r.Yield, err = yield(n["recipeYield"]); err != nil {
		return Recipe{}, err
	}
	if r.JSONLD, err = json.Marshal(n); err != nil {
		return Recipe{}, err
	}
	return r, nil
}

// cleanText decodes HTML entities, which recipe plugins often leave in
// JSON-LD text, and trims surrounding space.
func cleanText(s string) string {
	return strings.TrimSpace(html.UnescapeString(s))
}

func siteName(publisher any, g graph, ogSiteName string, base *url.URL) (string, error) {
	pubs, ok := publisher.([]any)
	if !ok {
		pubs = []any{publisher}
	}
	for _, p := range pubs {
		p, _, err := g.resolve(p)
		if err != nil {
			return "", fmt.Errorf("publisher: %w", err)
		}
		if m, ok := p.(map[string]any); ok {
			if name, ok := m["name"].(string); ok && cleanText(name) != "" {
				return cleanText(name), nil
			}
		}
	}
	if ogSiteName != "" {
		return ogSiteName, nil
	}
	return strings.TrimPrefix(base.Hostname(), "www."), nil
}

// imageURL picks the largest of the recipe's images by width × height;
// images without both dimensions count as size zero, and the first wins a
// tie, so a list of bare URLs gives its first.
func imageURL(v any, g graph, base *url.URL) (string, error) {
	items, ok := v.([]any)
	if !ok {
		if v == nil {
			return "", nil
		}
		items = []any{v}
	}
	best, bestArea := "", int64(-1)
	for _, item := range items {
		item, found, err := g.resolve(item)
		if err != nil {
			return "", fmt.Errorf("image: %w", err)
		}
		if !found {
			return "", fmt.Errorf("image references @id %v, which no node on the page has", item.(map[string]any)["@id"])
		}
		var u string
		var area int64
		switch img := item.(type) {
		case string:
			u = img
		case map[string]any:
			if s, ok := img["url"].(string); ok {
				u = s
			} else if s, ok := img["contentUrl"].(string); ok {
				u = s
			}
			w, err := dimension(img["width"])
			if err != nil {
				return "", fmt.Errorf("image width: %w", err)
			}
			h, err := dimension(img["height"])
			if err != nil {
				return "", fmt.Errorf("image height: %w", err)
			}
			area = w * h
		default:
			return "", fmt.Errorf("image is a %T, not a URL or ImageObject", item)
		}
		u = strings.TrimSpace(u)
		if u == "" {
			return "", errors.New("image has no URL")
		}
		if area > bestArea {
			best, bestArea = u, area
		}
	}
	if best == "" {
		return "", nil
	}
	ref, err := url.Parse(best)
	if err != nil {
		return "", fmt.Errorf("image URL %q: %w", best, err)
	}
	return base.ResolveReference(ref).String(), nil
}

// dimension reads an ImageObject width or height: a number or a string of
// digits; zero when absent.
func dimension(v any) (int64, error) {
	switch d := v.(type) {
	case nil:
		return 0, nil
	case json.Number:
		f, err := d.Float64()
		if err != nil || f < 0 {
			return 0, fmt.Errorf("%q is not a size", d)
		}
		return int64(f), nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(d), 10, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("%q is not a size", d)
		}
		return n, nil
	}
	return 0, fmt.Errorf("a %T is not a size", v)
}

func ingredients(v any) ([]string, error) {
	items, ok := v.([]any)
	if !ok {
		if v == nil {
			return nil, nil
		}
		items = []any{v}
	}
	var out []string
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("recipeIngredient holds a %T, not text", item)
		}
		if s = cleanText(s); s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// steps flattens recipeInstructions: one string of lines, or a list of
// strings, HowToSteps and HowToSections of those, in order.
func steps(v any) ([]string, error) {
	switch in := v.(type) {
	case nil:
		return nil, nil
	case string:
		var out []string
		for _, line := range strings.Split(in, "\n") {
			if line = cleanText(line); line != "" {
				out = append(out, line)
			}
		}
		return out, nil
	case map[string]any:
		return stepList([]any{in})
	case []any:
		return stepList(in)
	}
	return nil, fmt.Errorf("recipeInstructions is a %T", v)
}

func stepList(items []any) ([]string, error) {
	var out []string
	for _, item := range items {
		switch s := item.(type) {
		case string:
			if text := cleanText(s); text != "" {
				out = append(out, text)
			}
		case map[string]any:
			switch {
			case hasType(s, "HowToStep"):
				text, _ := s["text"].(string)
				if text = cleanText(text); text == "" {
					return nil, errors.New("HowToStep has no text")
				}
				out = append(out, text)
			case hasType(s, "HowToSection"):
				list, ok := s["itemListElement"].([]any)
				if !ok {
					return nil, errors.New("HowToSection has no itemListElement list")
				}
				section, err := stepList(list)
				if err != nil {
					return nil, err
				}
				out = append(out, section...)
			default:
				return nil, fmt.Errorf("recipeInstructions holds a %v, not a HowToStep or HowToSection", s["@type"])
			}
		default:
			return nil, fmt.Errorf("recipeInstructions holds a %T", item)
		}
	}
	return out, nil
}

// minutes reads a duration field: nil when absent or null; any string
// present, empty included, must be an ISO-8601 duration.
func minutes(n map[string]any, field string) (*int, error) {
	v, ok := n[field]
	if !ok || v == nil {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("%s is a %T, not an ISO-8601 duration", field, v)
	}
	m, err := isoMinutes(s)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	return &m, nil
}

// isoMinutes converts an ISO-8601 duration of weeks, days, hours, minutes
// and seconds to whole minutes, rounded. Years and months are refused:
// their length in minutes is not fixed.
func isoMinutes(s string) (int, error) {
	bad := fmt.Errorf("%q is not an ISO-8601 duration of days, hours, minutes or seconds", s)
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), "P")
	if !ok || rest == "" {
		return 0, bad
	}
	const units = "WDHMS"
	seconds := map[byte]float64{'W': 7 * 86400, 'D': 86400, 'H': 3600, 'M': 60, 'S': 1}
	inTime, last, total := false, -1, 0.0
	for rest != "" {
		if rest[0] == 'T' {
			if inTime || len(rest) == 1 {
				return 0, bad
			}
			inTime, rest = true, rest[1:]
			continue
		}
		i := 0
		for i < len(rest) && (rest[i] >= '0' && rest[i] <= '9' || rest[i] == '.' || rest[i] == ',') {
			i++
		}
		if i == 0 || i == len(rest) {
			return 0, bad
		}
		num, err := strconv.ParseFloat(strings.Replace(rest[:i], ",", ".", 1), 64)
		if err != nil {
			return 0, bad
		}
		unit := rest[i]
		pos := strings.IndexByte(units, unit)
		if pos < 0 || pos <= last || inTime != (pos >= 2) {
			return 0, bad
		}
		last, total, rest = pos, total+num*seconds[unit], rest[i+1:]
	}
	if last < 0 {
		return 0, bad
	}
	return int(math.Round(total / 60)), nil
}

func yield(v any) (string, error) {
	if list, ok := v.([]any); ok {
		if len(list) == 0 {
			return "", nil
		}
		v = list[0]
	}
	switch y := v.(type) {
	case nil:
		return "", nil
	case string:
		return cleanText(y), nil
	case json.Number:
		return y.String(), nil
	}
	return "", fmt.Errorf("recipeYield is a %T", v)
}

// htmlDoc is what Extract needs from a page's HTML.
type htmlDoc struct {
	jsonLD     []string
	ogSiteName string
}

// scanHTML is a small targeted scan, not a full HTML parser: it walks the
// tags, skipping comments and the bodies of scripts and styles, and keeps
// the bodies of application/ld+json scripts and the first og:site_name.
func scanHTML(doc string) htmlDoc {
	var out htmlDoc
	lower := asciiLower(doc)
	for pos := 0; ; {
		lt := strings.IndexByte(doc[pos:], '<')
		if lt < 0 {
			return out
		}
		pos += lt
		if strings.HasPrefix(doc[pos:], "<!--") {
			end := strings.Index(doc[pos+4:], "-->")
			if end < 0 {
				return out
			}
			pos += 4 + end + 3
			continue
		}
		name := tagName(lower[pos+1:])
		if name == "" {
			pos++
			continue
		}
		end := tagEnd(doc, pos+1+len(name))
		if end < 0 {
			return out
		}
		attrs := parseAttrs(doc[pos+1+len(name) : end])
		pos = end + 1
		switch name {
		case "meta":
			if out.ogSiteName == "" && attrs["property"] == "og:site_name" {
				out.ogSiteName = strings.TrimSpace(attrs["content"])
			}
		case "script", "style":
			closeAt := strings.Index(lower[pos:], "</"+name)
			if closeAt < 0 {
				return out
			}
			typ, _, _ := strings.Cut(attrs["type"], ";")
			if name == "script" && strings.EqualFold(strings.TrimSpace(typ), "application/ld+json") {
				out.jsonLD = append(out.jsonLD, doc[pos:pos+closeAt])
			}
			pos += closeAt
		}
	}
}

// asciiLower lower-cases ASCII letters only, so byte offsets match doc.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// tagName reads the lower-cased name of an opening tag at the start of s,
// or "" when s does not start one.
func tagName(s string) string {
	i := 0
	for i < len(s) && ('a' <= s[i] && s[i] <= 'z' || i > 0 && '0' <= s[i] && s[i] <= '9') {
		i++
	}
	if i == 0 || i < len(s) && !isSpace(s[i]) && s[i] != '>' && s[i] != '/' {
		return ""
	}
	return s[:i]
}

// tagEnd finds the '>' closing a tag whose attributes start at i, skipping
// quoted attribute values.
func tagEnd(doc string, i int) int {
	var quote byte
	for ; i < len(doc); i++ {
		switch c := doc[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return i
		}
	}
	return -1
}

// parseAttrs reads a tag's attributes; names are lower-cased, values are
// entity-decoded, and the first of a repeated name wins.
func parseAttrs(s string) map[string]string {
	attrs := map[string]string{}
	i := 0
	for i < len(s) {
		for i < len(s) && (isSpace(s[i]) || s[i] == '/') {
			i++
		}
		start := i
		for i < len(s) && !isSpace(s[i]) && s[i] != '=' && s[i] != '/' {
			i++
		}
		if i == start {
			i++
			continue
		}
		name := asciiLower(s[start:i])
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		value := ""
		if i < len(s) && s[i] == '=' {
			i++
			for i < len(s) && isSpace(s[i]) {
				i++
			}
			if i < len(s) && (s[i] == '"' || s[i] == '\'') {
				q := s[i]
				end := strings.IndexByte(s[i+1:], q)
				if end < 0 {
					end = len(s) - i - 1
				}
				value = s[i+1 : i+1+end]
				i += end + 2
			} else {
				start := i
				for i < len(s) && !isSpace(s[i]) {
					i++
				}
				value = s[start:i]
			}
		}
		if _, seen := attrs[name]; !seen {
			attrs[name] = html.UnescapeString(value)
		}
	}
	return attrs
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}
