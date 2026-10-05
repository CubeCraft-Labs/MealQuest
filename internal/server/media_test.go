package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// newMediaRouter returns a router over a photo cache whose data directory
// holds both sizes of one stored photo, and that photo's id. Each size's
// file holds its size as text.
func newMediaRouter(t *testing.T) (http.Handler, string) {
	t.Helper()
	dataDir := t.TempDir()
	photos, err := recipes.NewPhotoCache(recipes.NewFetcher(), newTestStore(t), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	id := store.NewID()
	for _, size := range []string{"400", "1200"} {
		p := filepath.Join(dataDir, "media", "photos", id+"-"+size+".jpg")
		if err := os.WriteFile(p, []byte("jpeg "+size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestRunner(t),
		photos, fstest.MapFS{"index.html": {Data: []byte(fixtureIndex)}})
	if err != nil {
		t.Fatal(err)
	}
	return r, id
}

func TestGetPhotoServesEachSize(t *testing.T) {
	t.Parallel()
	router, id := newMediaRouter(t)
	for _, size := range []string{"400", "1200"} {
		req := httptest.NewRequest(http.MethodGet, "/media/photos/"+id+"-"+size+".jpg", nil)
		req.AddCookie(validSessionCookie)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("size %s: status = %d, want 200", size, rec.Code)
		}
		if got := rec.Body.String(); got != "jpeg "+size {
			t.Errorf("size %s: body = %q", size, got)
		}
		if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
			t.Errorf("size %s: Content-Type = %q, want image/jpeg", size, got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "private, max-age=31536000, immutable" {
			t.Errorf("size %s: Cache-Control = %q", size, got)
		}
	}
}

func TestGetPhotoRequiresSession(t *testing.T) {
	t.Parallel()
	router, id := newMediaRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/media/photos/"+id+"-400.jpg", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := rec.Body.String(); got != unauthenticatedJSON {
		t.Errorf("body = %s, want %s", got, unauthenticatedJSON)
	}
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Errorf("Cache-Control = %q on a refused request", got)
	}
}

func TestGetPhotoUnknownNotFound(t *testing.T) {
	t.Parallel()
	router, id := newMediaRouter(t)
	for _, name := range []string{
		store.NewID() + "-400.jpg", // no such photo
		id + "-800.jpg",            // size not stored
		id + "-0400.jpg",
		id + "-+400.jpg",
		id + "-400.png",
		id + ".jpg",
		id + "-400",
		"..%2F" + id + "-400.jpg",
		"not-an-id-400.jpg",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/media/photos/"+name, nil)
			req.AddCookie(validSessionCookie)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
			if got := rec.Body.String(); got != notFoundJSON {
				t.Errorf("body = %s, want %s", got, notFoundJSON)
			}
			if got := rec.Header().Get("Cache-Control"); got != "" {
				t.Errorf("Cache-Control = %q on a 404", got)
			}
		})
	}
}
