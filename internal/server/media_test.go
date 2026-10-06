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

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// newMediaRouter returns a router over a photo cache whose data directory
// holds both sizes of two photos, the id of the one with a photos row, and
// the id of the orphan without one. Each size's file holds its size as
// text.
func newMediaRouter(t *testing.T) (r http.Handler, id, orphan string) {
	t.Helper()
	dataDir := t.TempDir()
	st := newTestStore(t)
	photos, err := recipes.NewPhotoCache(recipes.NewFetcher(), st, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	id, orphan = store.NewID(), store.NewID()
	for _, stem := range []string{id, orphan} {
		for _, size := range []string{"400", "1200"} {
			p := filepath.Join(dataDir, "media", "photos", stem+"-"+size+".jpg")
			if err := os.WriteFile(p, []byte("jpeg "+size), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	now := store.Now().String()
	if _, err := db.New(st.Writer).CreatePhoto(t.Context(), db.CreatePhotoParams{
		ID:        id,
		SourceUrl: "https://example.com/a.jpg",
		LocalPath: "media/photos/" + id + "-1200.jpg",
		Width:     1600,
		Height:    1200,
		Sha256:    "sha",
		FetchedAt: now,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err = NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t), newTestDinners(t), newTestRunner(t),
		photos, fstest.MapFS{"index.html": {Data: []byte(fixtureIndex)}})
	if err != nil {
		t.Fatal(err)
	}
	return r, id, orphan
}

func TestGetPhotoServesEachSize(t *testing.T) {
	t.Parallel()
	router, id, _ := newMediaRouter(t)
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
	router, id, _ := newMediaRouter(t)
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
	router, id, orphan := newMediaRouter(t)
	for _, name := range []string{
		store.NewID() + "-400.jpg", // no such photo
		orphan + "-400.jpg",        // file stored, no photos row
		id + "-800.jpg",            // size not stored
		orphan + "-1200.jpg",
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
