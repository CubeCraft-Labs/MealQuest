package dinners

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
)

// Detail is a dinner with its recipe, as the recipe detail screen and
// Kitchen reading mode show it.
type Detail struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Status       string  `json:"status"`
	Origin       string  `json:"origin"`
	Method       *string `json:"method"`
	MinutesTotal *int64  `json:"minutes_total"`
	Difficulty   *string `json:"difficulty"`
	Cuisine      *string `json:"cuisine"`
	IsFavorite   bool    `json:"is_favorite"`
	// PhotoURLs is the cached photo at each stored width, keyed by width
	// ("400", "1200"); null when the dinner has no photo.
	PhotoURLs map[string]string `json:"photo_urls"`
	// Ingredients and Steps are the recipe's, in recipe order; empty when
	// the dinner has no recipe.
	Ingredients []Ingredient `json:"ingredients"`
	Steps       []string     `json:"steps"`
	// Source is the site of the recipe page the dinner is grounded in;
	// null for a recipe entered by hand or a dinner with no recipe.
	Source          *Source `json:"source"`
	GroundingStatus string  `json:"grounding_status"`
	// GroundingError is why grounding failed, one of the Reason codes;
	// null unless GroundingStatus is failed.
	GroundingError *string `json:"grounding_error"`
	// WhyPicked is the reason of the suggestion that created the dinner
	// (see GetDinnerSuggestionReasons); null for a dinner no suggestion
	// created.
	WhyPicked *string `json:"why_picked"`
}

// Ingredient is one recipe line. RawText is the line as the recipe gives
// it; Name, Qty, Unit and Note are normalize_recipe's reading of it, each
// null when it has none, and Name, Qty and Unit always null for a recipe
// entered by hand (see IsManualRecipe).
type Ingredient struct {
	Name    *string `json:"name"`
	Qty     *string `json:"qty"`
	Unit    *string `json:"unit"`
	Note    *string `json:"note"`
	RawText string  `json:"raw_text"`
}

// Source is the site a recipe was fetched from. SiteName is the name the
// page itself gives its site, and null when it gives none or the stored
// name could have come from a link (see pageSiteName). URL is the page's
// link only when it is verifiably free of secrets (see safeSourceURL), and
// null otherwise: a link pasted or offered as a candidate may carry a
// personal token, so a client links the credit only when URL is not null.
// Source is set for every fetched recipe, so a client always credits it:
// by SiteName, else by URL's host, else without naming the site.
type Source struct {
	SiteName *string `json:"site_name"`
	URL      *string `json:"url"`
}

// Get returns dinner id with its recipe and the reason it was suggested,
// read in one read transaction. An unknown or archived dinner is
// ErrNotFound: an archived dinner has no detail to show.
func (s *Service) Get(ctx context.Context, id string) (d Detail, err error) {
	tx, err := s.st.Reader.BeginTx(ctx, nil)
	if err != nil {
		return Detail{}, fmt.Errorf("dinners: begin read: %w", err)
	}
	defer func() {
		err = errors.Join(err, tx.Rollback())
	}()
	q := db.New(tx)
	row, err := q.GetDinner(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Detail{}, ErrNotFound
	}
	if err != nil {
		return Detail{}, fmt.Errorf("dinners: read %s: %w", id, err)
	}
	if row.Status == StatusArchived {
		return Detail{}, ErrNotFound
	}
	d = Detail{
		ID:              row.ID,
		Name:            row.Name,
		Status:          row.Status,
		Origin:          row.Origin,
		Method:          nullString(row.Method),
		Difficulty:      nullString(row.Difficulty),
		Cuisine:         nullString(row.Cuisine),
		IsFavorite:      row.IsFavorite == 1,
		Ingredients:     []Ingredient{},
		Steps:           []string{},
		GroundingStatus: row.GroundingStatus,
		GroundingError:  nullString(row.GroundingError),
	}
	if row.MinutesTotal.Valid {
		d.MinutesTotal = &row.MinutesTotal.Int64
	}
	if row.PhotoID.Valid {
		d.PhotoURLs = make(map[string]string, len(recipes.PhotoSizes))
		for _, size := range recipes.PhotoSizes {
			d.PhotoURLs[strconv.Itoa(size)] = photoURL(row.PhotoID.String, size)
		}
	}
	if (row.GroundingStatus == GroundingGrounded || row.GroundingStatus == GroundingManual) && !row.RecipeID.Valid {
		return Detail{}, fmt.Errorf("dinners: %s is %s with no recipe", id, row.GroundingStatus)
	}
	if row.RecipeID.Valid {
		if err := readRecipe(ctx, q, row.RecipeID.String, &d); err != nil {
			return Detail{}, fmt.Errorf("dinners: read recipe of %s: %w", id, err)
		}
	}
	reasons, err := q.GetDinnerSuggestionReasons(ctx, id)
	if err != nil {
		return Detail{}, fmt.Errorf("dinners: read suggestion reason of %s: %w", id, err)
	}
	switch len(reasons) {
	case 0:
	case 1:
		d.WhyPicked = &reasons[0]
	default:
		// Nothing records which of them created the dinner, and any
		// one picked would be a guess.
		return Detail{}, fmt.Errorf("dinners: %s has %d suggestions that could have created it", id, len(reasons))
	}
	return d, nil
}

// readRecipe sets d's Source, Ingredients and Steps from recipe recipeID.
func readRecipe(ctx context.Context, q *db.Queries, recipeID string, d *Detail) error {
	rec, err := q.GetRecipe(ctx, recipeID)
	if err != nil {
		return fmt.Errorf("recipe %s: %w", recipeID, err)
	}
	if !IsManualRecipe(rec.SourceUrl) {
		src, err := SourceOf(rec.SiteName, rec.SourceUrl)
		if err != nil {
			return err
		}
		d.Source = &src
	}
	ings, err := q.ListRecipeIngredients(ctx, recipeID)
	if err != nil {
		return fmt.Errorf("ingredients: %w", err)
	}
	for _, ing := range ings {
		d.Ingredients = append(d.Ingredients, Ingredient{
			Name:    nullString(ing.Name),
			Qty:     nullString(ing.Qty),
			Unit:    nullString(ing.Unit),
			Note:    nullString(ing.Note),
			RawText: ing.RawText,
		})
	}
	steps, err := q.ListRecipeSteps(ctx, recipeID)
	if err != nil {
		return fmt.Errorf("steps: %w", err)
	}
	for _, step := range steps {
		d.Steps = append(d.Steps, step.Text)
	}
	return nil
}

// SourceOf is the Source of a recipe fetched from sourceURL whose stored
// site name is siteName: the name only when the page gave it (see
// pageSiteName) and the link only when it is free of secrets (see
// safeSourceURL). It is the one way a fetched recipe's source reaches a
// client. A sourceURL that does not parse is an error, whether or not a
// site name is stored.
func SourceOf(siteName sql.NullString, sourceURL string) (Source, error) {
	link, err := url.Parse(sourceURL)
	if err != nil {
		// Not wrapped: url.Parse's error quotes the link.
		return Source{}, errors.New("source url does not parse")
	}
	return Source{SiteName: pageSiteName(siteName, link), URL: safeSourceURL(sourceURL)}, nil
}

// pageSiteName is the stored site name of a recipe fetched from link
// when it is one the page gave, else nil. A page that names no site is
// stored under recipes.HostSiteName of the URL it was served from: the
// pasted link's host, which may hold a personal token (a private
// subdomain), or that of a page the link redirected to, which is not
// stored. Nothing records which a stored name is, so any name that could
// be a host — the link's own host, an IP address, or a dotted host name —
// is treated as one and withheld. A site that names itself as a domain,
// such as "Food.com", loses its name with them: a credit that does not
// name the site is better than one that leaks a link.
func pageSiteName(stored sql.NullString, link *url.URL) *string {
	if !stored.Valid {
		return nil
	}
	name := stored.String
	if strings.EqualFold(name, recipes.HostSiteName(link)) || net.ParseIP(name) != nil {
		return nil
	}
	if u, err := url.Parse("https://" + name + "/"); err == nil && u.Host == name && strings.Contains(name, ".") {
		return nil
	}
	return &name
}

// safeSourceURL is sourceURL when it is verifiably free of secrets — an
// https link to a host, with no user or password, no query (not even a
// bare "?") and no fragment (not even a bare "#") — else nil. Those are
// the places a link carries a token; a link failing any check, or not
// parsing, is withheld rather than trimmed, since a trimmed link is not the
// page the recipe came from. The client (sourceUrl in
// web/src/dinners/detail.ts) parses the link with the browser's URL parser
// and refuses the whole recipe when it fails, so the host and port must
// also be ones it parses: see browserHost.
func safeSourceURL(sourceURL string) *string {
	u, err := url.Parse(sourceURL)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(sourceURL, "#") {
		return nil
	}
	// url.Parse decodes a percent-encoded host; the link must spell its
	// host out as it is checked.
	authority, _, _ := strings.Cut(sourceURL[len(u.Scheme)+len("://"):], "/")
	if authority != u.Host || !browserHost(u) {
		return nil
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
			return nil
		}
	}
	return &sourceURL
}

// browserHost says whether u's host is one the browser's URL parser (the
// WHATWG URL Standard) accepts as it stands, where url.Parse accepts more,
// such as "[bad]". The rule is narrower than the browser's: a bracketed
// host is an IPv6 address without a zone; any other is ASCII letters,
// digits and hyphens in dot-separated labels, none empty but a trailing
// one, none punycode ("xn--"), since the browser refuses invalid punycode
// and checking it is not worth a decoder; and a host whose last label is
// a number, which the browser reads as an IPv4 address, must be a dotted
// quad. A link to any other host is withheld.
func browserHost(u *url.URL) bool {
	host := u.Hostname()
	if strings.HasPrefix(u.Host, "[") {
		ip := net.ParseIP(host)
		return ip != nil && strings.Contains(host, ":")
	}
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	for _, label := range labels {
		if label == "" || len(label) >= 4 && strings.EqualFold(label[:4], "xn--") {
			return false
		}
		for _, c := range label {
			if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-') {
				return false
			}
		}
	}
	last := labels[len(labels)-1]
	if strings.Trim(last, "0123456789") == "" || len(last) >= 2 && strings.EqualFold(last[:2], "0x") {
		ip := net.ParseIP(host)
		return ip != nil && ip.To4() != nil && !strings.Contains(host, ":")
	}
	return true
}
