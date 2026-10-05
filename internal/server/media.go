package server

import (
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// photoCacheControl lets the browser keep a photo for a year without
// revalidating: a photo's files never change once stored, and a new image
// gets a new id.
const photoCacheControl = "private, max-age=31536000, immutable"

// PhotoFiles opens a cached photo's file at one of its stored sizes. An
// unknown photo or size is an error matching fs.ErrNotExist.
type PhotoFiles interface {
	OpenPhoto(id string, size int) (*os.File, error)
}

// getPhoto answers GET /media/photos/{id}-{size}.jpg with the stored JPEG.
func getPhoto(logger *slog.Logger, photos PhotoFiles) http.HandlerFunc {
	notFound := apiErrorHandler(logger, http.StatusNotFound, "not_found", "not found")
	return func(w http.ResponseWriter, r *http.Request) {
		id, size, ok := parsePhotoName(chi.URLParam(r, "name"))
		if !ok {
			notFound(w, r)
			return
		}
		f, err := photos.OpenPhoto(id, size)
		if errors.Is(err, fs.ErrNotExist) {
			notFound(w, r)
			return
		}
		if err != nil {
			logger.ErrorContext(r.Context(), "open photo", "id", id, "size", size, "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			logger.ErrorContext(r.Context(), "stat photo", "id", id, "size", size, "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", photoCacheControl)
		http.ServeContent(w, r, "", info.ModTime(), f)
	}
}

// parsePhotoName splits "{id}-{size}.jpg" into its id and size, refusing
// a size not written in its canonical decimal form.
func parsePhotoName(name string) (id string, size int, ok bool) {
	stem, ok := strings.CutSuffix(name, ".jpg")
	if !ok {
		return "", 0, false
	}
	i := strings.LastIndexByte(stem, '-')
	if i < 0 {
		return "", 0, false
	}
	size, err := strconv.Atoi(stem[i+1:])
	if err != nil || strconv.Itoa(size) != stem[i+1:] {
		return "", 0, false
	}
	return stem[:i], size, true
}
