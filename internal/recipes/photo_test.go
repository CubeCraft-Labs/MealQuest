package recipes

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

// photoServer serves each path's body with its Content-Type and counts
// requests.
type photoServer struct {
	files    map[string]photoFile
	requests atomic.Int32
}

type photoFile struct {
	contentType string
	body        []byte
}

func (s *photoServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.requests.Add(1)
	f, ok := s.files[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", f.contentType)
	w.Write(f.body)
}

// newTestPhotoCache returns a PhotoCache over files, a migrated temp
// database and a temp data directory.
func newTestPhotoCache(t *testing.T, files map[string]photoFile) (*PhotoCache, *store.Store, string, *photoServer) {
	t.Helper()
	srv := &photoServer{files: files}
	env := newTestEnv(t, srv, publicAnswers())
	installClock(env.f)
	dataDir := t.TempDir()
	st, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Migrate(t.Context(), st.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}
	cache, err := NewPhotoCache(env.f, st, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	return cache, st, dataDir, srv
}

// gradient is a w×h opaque image.
func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 128, 255})
		}
	}
	return img
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// twoFrameGIF is a w×h animated GIF whose first frame is red and second
// blue.
func twoFrameGIF(t *testing.T, w, h int) []byte {
	t.Helper()
	frame := func(c color.Color) *image.Paletted {
		p := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{c})
		return p
	}
	var b bytes.Buffer
	anim := &gif.GIF{
		Image: []*image.Paletted{frame(color.RGBA{255, 0, 0, 255}), frame(color.RGBA{0, 0, 255, 255})},
		Delay: []int{10, 10},
	}
	if err := gif.EncodeAll(&b, anim); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// storedSize decodes the stored JPEG of id at size and returns it.
func storedSize(t *testing.T, dataDir, id string, size int) image.Image {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dataDir, "media", "photos", photoFileName(id, size)))
	if err != nil {
		t.Fatalf("read stored photo: %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(b))
	if err != nil || format != "jpeg" {
		t.Fatalf("stored photo is %q, %v; want a JPEG", format, err)
	}
	return img
}

func TestCachePhotoFormatsAndSizes(t *testing.T) {
	t.Parallel()
	files := map[string]photoFile{
		"/landscape.jpg": {"image/jpeg", encodeJPEG(t, gradient(800, 600))},
		"/portrait.png":  {"image/png", encodePNG(t, gradient(600, 900))},
		"/photo.webp":    {"image/webp", readTestdata(t, "photo.webp")},
		"/anim.gif":      {"image/gif; charset=binary", twoFrameGIF(t, 300, 450)},
		"/square.png":    {"image/png", encodePNG(t, gradient(1500, 1500))},
	}
	cache, _, dataDir, _ := newTestPhotoCache(t, files)

	tests := []struct {
		path          string
		width, height int64
		// sizes maps each stored size to its expected dimensions.
		sizes map[int]image.Point
	}{
		{"/landscape.jpg", 800, 600, map[int]image.Point{1200: {1200, 900}, 400: {400, 300}}},
		{"/portrait.png", 600, 900, map[int]image.Point{1200: {800, 1200}, 400: {267, 400}}},
		{"/photo.webp", 640, 480, map[int]image.Point{1200: {1200, 900}, 400: {400, 300}}},
		{"/anim.gif", 300, 450, map[int]image.Point{1200: {800, 1200}, 400: {267, 400}}},
		{"/square.png", 1500, 1500, map[int]image.Point{1200: {1200, 1200}, 400: {400, 400}}},
	}
	for _, tc := range tests {
		photo, err := cache.CachePhoto(t.Context(), "https://example.com"+tc.path)
		if err != nil {
			t.Fatalf("CachePhoto %s: %v", tc.path, err)
		}
		if photo.Width != tc.width || photo.Height != tc.height {
			t.Errorf("%s: row is %dx%d, want %dx%d", tc.path, photo.Width, photo.Height, tc.width, tc.height)
		}
		if photo.SourceUrl != "https://example.com"+tc.path || len(photo.Sha256) != 64 {
			t.Errorf("%s: row = %+v", tc.path, photo)
		}
		if want := "media/photos/" + photo.ID + "-1200.jpg"; photo.LocalPath != want {
			t.Errorf("%s: local_path = %q, want %q", tc.path, photo.LocalPath, want)
		}
		for size, want := range tc.sizes {
			img := storedSize(t, dataDir, photo.ID, size)
			if got := img.Bounds().Size(); got != want {
				t.Errorf("%s at %d: stored %v, want %v", tc.path, size, got, want)
			}
		}
	}
}

func TestCachePhotoGIFUsesFirstFrame(t *testing.T) {
	t.Parallel()
	cache, _, dataDir, _ := newTestPhotoCache(t, map[string]photoFile{
		"/anim.gif": {"image/gif", twoFrameGIF(t, 400, 400)},
	})
	photo, err := cache.CachePhoto(t.Context(), "https://example.com/anim.gif")
	if err != nil {
		t.Fatalf("CachePhoto: %v", err)
	}
	r, g, b, _ := storedSize(t, dataDir, photo.ID, 400).At(200, 200).RGBA()
	if r>>8 < 200 || g>>8 > 50 || b>>8 > 50 {
		t.Errorf("centre pixel = (%d,%d,%d), want the first frame's red", r>>8, g>>8, b>>8)
	}
}

func TestCachePhotoFillsTransparencyWhite(t *testing.T) {
	t.Parallel()
	cache, _, dataDir, _ := newTestPhotoCache(t, map[string]photoFile{
		"/clear.png": {"image/png", encodePNG(t, image.NewNRGBA(image.Rect(0, 0, 400, 400)))},
	})
	photo, err := cache.CachePhoto(t.Context(), "https://example.com/clear.png")
	if err != nil {
		t.Fatalf("CachePhoto: %v", err)
	}
	r, g, b, _ := storedSize(t, dataDir, photo.ID, 400).At(200, 200).RGBA()
	if r>>8 < 250 || g>>8 < 250 || b>>8 < 250 {
		t.Errorf("transparent pixel stored as (%d,%d,%d), want white", r>>8, g>>8, b>>8)
	}
}

func countPhotos(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.Reader.QueryRowContext(context.Background(), "SELECT count(*) FROM photos").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func photoFiles(t *testing.T, dataDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, "media", "photos"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestCachePhotoRejectsTooSmall(t *testing.T) {
	t.Parallel()
	cache, st, dataDir, _ := newTestPhotoCache(t, map[string]photoFile{
		"/narrow.png": {"image/png", encodePNG(t, gradient(299, 1000))},
		"/short.jpg":  {"image/jpeg", encodeJPEG(t, gradient(1000, 299))},
		"/edge.png":   {"image/png", encodePNG(t, gradient(300, 1000))},
	})
	for _, p := range []string{"/narrow.png", "/short.jpg"} {
		if _, err := cache.CachePhoto(t.Context(), "https://example.com"+p); !errors.Is(err, ErrPhotoTooSmall) {
			t.Errorf("CachePhoto %s error = %v, want ErrPhotoTooSmall", p, err)
		}
	}
	if n := countPhotos(t, st); n != 0 {
		t.Errorf("%d photos recorded for refused images", n)
	}
	if names := photoFiles(t, dataDir); len(names) != 0 {
		t.Errorf("files stored for refused images: %v", names)
	}
	if _, err := cache.CachePhoto(t.Context(), "https://example.com/edge.png"); err != nil {
		t.Errorf("CachePhoto with a 300 px short side: %v", err)
	}
}

func TestCachePhotoRejectsNonImages(t *testing.T) {
	t.Parallel()
	cache, st, _, _ := newTestPhotoCache(t, map[string]photoFile{
		"/page.html": {"text/html", encodePNG(t, gradient(400, 400))},
		"/fake.jpg":  {"image/jpeg", []byte("<html>not a picture</html>")},
	})
	if _, err := cache.CachePhoto(t.Context(), "https://example.com/page.html"); !errors.Is(err, ErrNotImage) {
		t.Errorf("text/html error = %v, want ErrNotImage", err)
	}
	if _, err := cache.CachePhoto(t.Context(), "https://example.com/fake.jpg"); !errors.Is(err, ErrPhotoFormat) {
		t.Errorf("undecodable image error = %v, want ErrPhotoFormat", err)
	}
	if n := countPhotos(t, st); n != 0 {
		t.Errorf("%d photos recorded", n)
	}
}

func TestCachePhotoDeduplicatesBySHA256(t *testing.T) {
	t.Parallel()
	body := encodeJPEG(t, gradient(640, 480))
	cache, st, dataDir, srv := newTestPhotoCache(t, map[string]photoFile{
		"/a.jpg":     {"image/jpeg", body},
		"/b.jpg":     {"image/jpeg", body},
		"/other.jpg": {"image/jpeg", encodeJPEG(t, gradient(480, 640))},
	})
	first, err := cache.CachePhoto(t.Context(), "https://example.com/a.jpg")
	if err != nil {
		t.Fatalf("CachePhoto a: %v", err)
	}
	again, err := cache.CachePhoto(t.Context(), "https://example.com/b.jpg")
	if err != nil {
		t.Fatalf("CachePhoto b: %v", err)
	}
	if again != first {
		t.Errorf("same bytes at another URL = %+v, want the first row %+v", again, first)
	}
	if n := srv.requests.Load(); n != 2 {
		t.Errorf("%d downloads, want 2: each URL is fetched to hash it", n)
	}
	other, err := cache.CachePhoto(t.Context(), "https://example.com/other.jpg")
	if err != nil {
		t.Fatalf("CachePhoto other: %v", err)
	}
	if other.ID == first.ID || other.Sha256 == first.Sha256 {
		t.Errorf("different bytes share a photo: %+v and %+v", first, other)
	}
	if n := countPhotos(t, st); n != 2 {
		t.Errorf("%d photos recorded, want 2", n)
	}
	if names := photoFiles(t, dataDir); len(names) != 4 {
		t.Errorf("stored files %v, want two sizes of two photos", names)
	}
}

func TestOpenPhoto(t *testing.T) {
	t.Parallel()
	cache, _, _, _ := newTestPhotoCache(t, map[string]photoFile{
		"/a.png": {"image/png", encodePNG(t, gradient(400, 300))},
	})
	photo, err := cache.CachePhoto(t.Context(), "https://example.com/a.png")
	if err != nil {
		t.Fatalf("CachePhoto: %v", err)
	}
	for _, size := range PhotoSizes {
		f, err := cache.OpenPhoto(photo.ID, size)
		if err != nil {
			t.Fatalf("OpenPhoto %d: %v", size, err)
		}
		f.Close()
	}
	for _, tc := range []struct {
		id   string
		size int
	}{
		{photo.ID, 800},
		{store.NewID(), 400},
		{strings.ToLower(photo.ID), 400},
		{"../" + photo.ID, 400},
		{"", 400},
	} {
		if _, err := cache.OpenPhoto(tc.id, tc.size); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("OpenPhoto(%q, %d) error = %v, want fs.ErrNotExist", tc.id, tc.size, err)
		}
	}
}

func TestFetchImageSizeCap(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		size := MaxImageBytes
		if r.URL.Path != "/exact" {
			size++
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(bytes.Repeat([]byte("a"), size))
	}), publicAnswers())
	installClock(env.f)

	page, err := env.f.FetchImage(t.Context(), "https://example.com/exact")
	if err != nil {
		t.Fatalf("FetchImage at the cap: %v", err)
	}
	if len(page.Body) != MaxImageBytes {
		t.Errorf("body is %d bytes, want %d", len(page.Body), MaxImageBytes)
	}
	if _, err := env.f.FetchImage(t.Context(), "https://example.com/over"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("FetchImage over the cap error = %v, want ErrTooLarge", err)
	}
}

func TestFetchImageContentTypes(t *testing.T) {
	t.Parallel()
	var accept atomic.Value
	env := newTestEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept.Store(r.Header.Get("Accept"))
		w.Header().Set("Content-Type", r.URL.Query().Get("type"))
		w.Write([]byte("x"))
	}), publicAnswers())
	installClock(env.f)
	typeURL := func(ct string) string { return "https://example.com/?type=" + url.QueryEscape(ct) }

	for _, ct := range []string{"image/jpeg", "image/png", "image/webp", "image/gif", "IMAGE/PNG; q=1"} {
		if _, err := env.f.FetchImage(t.Context(), typeURL(ct)); err != nil {
			t.Errorf("FetchImage %q: %v", ct, err)
		}
	}
	if got := accept.Load(); got != "image/jpeg,image/png,image/webp,image/gif" {
		t.Errorf("Accept = %q", got)
	}
	for _, ct := range []string{"text/html", "image/svg+xml", "image/avif", "application/octet-stream", ""} {
		if _, err := env.f.FetchImage(t.Context(), typeURL(ct)); !errors.Is(err, ErrNotImage) {
			t.Errorf("FetchImage %q error = %v, want ErrNotImage", ct, err)
		}
	}
	// Pages still fetch whatever the Content-Type.
	if _, err := env.f.Fetch(t.Context(), typeURL("text/plain")); err != nil {
		t.Errorf("Fetch: %v", err)
	}
}
