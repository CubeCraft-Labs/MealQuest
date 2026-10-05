package recipes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"sync"

	"github.com/oklog/ulid/v2"
	"golang.org/x/image/draw"
	"golang.org/x/image/webp"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

const (
	// PhotoDir is where cached photos are stored, relative to the data
	// directory.
	PhotoDir = "media/photos"
	// PhotoMinShortSide is the least a photo's shorter side may measure, in
	// pixels.
	PhotoMinShortSide = 300
	// PhotoMaxPixels bounds the pixels a photo may decode to, so a small
	// compressed file cannot claim gigabytes of memory.
	PhotoMaxPixels = 40_000_000
	// PhotoQuality is the JPEG quality of every stored size.
	PhotoQuality = 82
)

// PhotoSizes are the long-side lengths, in pixels, each photo is stored at.
var PhotoSizes = []int{1200, 400}

var (
	// ErrPhotoTooSmall is returned for an image whose shorter side is under
	// PhotoMinShortSide.
	ErrPhotoTooSmall = errors.New("recipes: photo is too small")
	// ErrPhotoTooLarge is returned for an image over PhotoMaxPixels.
	ErrPhotoTooLarge = errors.New("recipes: photo has too many pixels")
	// ErrPhotoFormat is returned for bytes that are not a JPEG, PNG, WebP
	// or GIF image.
	ErrPhotoFormat = errors.New("recipes: photo is not a JPEG, PNG, WebP or GIF image")
)

// photoDecoders are the formats a photo may be in. A GIF decodes to its
// first frame.
var photoDecoders = map[string]func([]byte) (image.Image, error){
	"jpeg": func(b []byte) (image.Image, error) { return jpeg.Decode(bytes.NewReader(b)) },
	"png":  func(b []byte) (image.Image, error) { return png.Decode(bytes.NewReader(b)) },
	"gif":  func(b []byte) (image.Image, error) { return gif.Decode(bytes.NewReader(b)) },
	"webp": func(b []byte) (image.Image, error) { return webp.Decode(bytes.NewReader(b)) },
}

// PhotoCache downloads photos through a Fetcher and stores each at every
// PhotoSizes length as {id}-{size}.jpg under PhotoDir, with a photos row.
// Photos are deduplicated by the SHA-256 of the downloaded bytes. A
// PhotoCache is safe for concurrent use; photos are processed one at a time.
type PhotoCache struct {
	fetcher *Fetcher
	store   *store.Store
	// dir is the absolute photo directory.
	dir string

	// mu serialises the dedupe lookup, processing and insert, so one image
	// is never stored twice.
	mu sync.Mutex
}

// NewPhotoCache returns a PhotoCache storing photos under dataDir, creating
// the photo directory if it is absent.
func NewPhotoCache(fetcher *Fetcher, st *store.Store, dataDir string) (*PhotoCache, error) {
	if fetcher == nil {
		return nil, errors.New("recipes: nil fetcher")
	}
	if st == nil {
		return nil, errors.New("recipes: nil store")
	}
	dir, err := filepath.Abs(filepath.Join(dataDir, filepath.FromSlash(PhotoDir)))
	if err != nil {
		return nil, fmt.Errorf("recipes: resolve photo directory: %w", err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("recipes: create photo directory: %w", err)
	}
	return &PhotoCache{fetcher: fetcher, store: st, dir: dir}, nil
}

// CachePhoto downloads the image at rawURL and returns its photos row. An
// image whose bytes are already cached returns the existing row. Otherwise
// it is decoded, refused if its shorter side is under PhotoMinShortSide,
// scaled so its long side is each of PhotoSizes, encoded as JPEG and
// recorded. The row's width and height are the original image's, and its
// local_path is the largest size's file relative to the data directory.
func (c *PhotoCache) CachePhoto(ctx context.Context, rawURL string) (db.Photo, error) {
	page, err := c.fetcher.FetchImage(ctx, rawURL)
	if err != nil {
		return db.Photo{}, err
	}
	digest := sha256.Sum256(page.Body)
	sum := hex.EncodeToString(digest[:])

	c.mu.Lock()
	defer c.mu.Unlock()
	existing, err := db.New(c.store.Reader).GetPhotoBySHA256(ctx, sum)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return db.Photo{}, fmt.Errorf("recipes: look up photo: %w", err)
	}

	img, err := decodePhoto(page.Body)
	if err != nil {
		return db.Photo{}, fmt.Errorf("%w: %s", err, page.URL)
	}
	id := store.NewID()
	var written []string
	removeWritten := func() error {
		var errs []error
		for _, p := range written {
			errs = append(errs, os.Remove(p))
		}
		return errors.Join(errs...)
	}
	for _, size := range PhotoSizes {
		p := c.path(id, size)
		if err := writeJPEG(p, scaleLongSide(img, size)); err != nil {
			return db.Photo{}, errors.Join(err, removeWritten())
		}
		written = append(written, p)
	}

	now := store.Now().String()
	bounds := img.Bounds()
	photo, err := db.New(c.store.Writer).CreatePhoto(ctx, db.CreatePhotoParams{
		ID:        id,
		SourceUrl: page.URL,
		LocalPath: path.Join(PhotoDir, photoFileName(id, PhotoSizes[0])),
		Width:     int64(bounds.Dx()),
		Height:    int64(bounds.Dy()),
		Sha256:    sum,
		FetchedAt: now,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		return db.Photo{}, errors.Join(fmt.Errorf("recipes: record photo: %w", err), removeWritten())
	}
	return photo, nil
}

// OpenPhoto opens the stored file of photo id at size. An id that is not a
// canonical ULID, a size not in PhotoSizes, or a photo that is not stored
// is an error matching fs.ErrNotExist.
func (c *PhotoCache) OpenPhoto(id string, size int) (*os.File, error) {
	parsed, err := ulid.ParseStrict(id)
	if err != nil || parsed.String() != id || !slices.Contains(PhotoSizes, size) {
		return nil, fmt.Errorf("recipes: photo %q at size %d: %w", id, size, fs.ErrNotExist)
	}
	return os.Open(c.path(id, size))
}

func (c *PhotoCache) path(id string, size int) string {
	return filepath.Join(c.dir, photoFileName(id, size))
}

func photoFileName(id string, size int) string {
	return id + "-" + strconv.Itoa(size) + ".jpg"
}

// decodePhoto decodes a JPEG, PNG, WebP or GIF image, checking its size
// from the header before decoding the pixels.
func decodePhoto(b []byte) (image.Image, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPhotoFormat, err)
	}
	decode, ok := photoDecoders[format]
	if !ok {
		return nil, fmt.Errorf("%w: format %q", ErrPhotoFormat, format)
	}
	if int64(cfg.Width)*int64(cfg.Height) > PhotoMaxPixels {
		return nil, fmt.Errorf("%w: %dx%d", ErrPhotoTooLarge, cfg.Width, cfg.Height)
	}
	if min(cfg.Width, cfg.Height) < PhotoMinShortSide {
		return nil, fmt.Errorf("%w: %dx%d, shorter side under %d px", ErrPhotoTooSmall, cfg.Width, cfg.Height, PhotoMinShortSide)
	}
	img, err := decode(b)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPhotoFormat, err)
	}
	return img, nil
}

// scaleLongSide returns img scaled so its longer side is long pixels,
// keeping its aspect ratio. Transparent areas become white, since JPEG has
// no alpha.
func scaleLongSide(img image.Image, long int) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := long, long
	if w >= h {
		dh = max(1, int(math.Round(float64(h)*float64(long)/float64(w))))
	} else {
		dw = max(1, int(math.Round(float64(w)*float64(long)/float64(h))))
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

// writeJPEG encodes img at PhotoQuality into a temporary file beside path
// and renames it into place, so a reader never sees a partial file.
func writeJPEG(path string, img image.Image) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".photo-*.tmp")
	if err != nil {
		return fmt.Errorf("recipes: create photo file: %w", err)
	}
	tmp := f.Name()
	err = jpeg.Encode(f, img, &jpeg.Options{Quality: PhotoQuality})
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		return errors.Join(fmt.Errorf("recipes: write photo %s: %w", filepath.Base(path), err), os.Remove(tmp))
	}
	return nil
}
