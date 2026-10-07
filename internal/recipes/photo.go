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

// ImageFetcher fetches an image; *Fetcher satisfies it.
type ImageFetcher interface {
	FetchImage(ctx context.Context, rawURL string) (Page, error)
}

// PhotoCache downloads photos through an ImageFetcher and stores each at every
// PhotoSizes length as {id}-{size}.jpg under PhotoDir, with a photos row.
// Photos are deduplicated by the SHA-256 of the downloaded bytes. A
// PhotoCache is safe for concurrent use.
type PhotoCache struct {
	images ImageFetcher
	store  *store.Store
	// dir is the absolute photo directory.
	dir string
}

// NewPhotoCache returns a PhotoCache downloading images with images and
// storing photos under dataDir, creating the photo directory if it is
// absent.
func NewPhotoCache(images ImageFetcher, st *store.Store, dataDir string) (*PhotoCache, error) {
	if images == nil {
		return nil, errors.New("recipes: nil image fetcher")
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
	return &PhotoCache{images: images, store: st, dir: dir}, nil
}

// DownloadedPhoto is an image DownloadPhoto has downloaded and decoded,
// ready for CachePhoto.
type DownloadedPhoto struct {
	// url is the image's final URL, after redirects.
	url    string
	sha256 string
	img    image.Image
}

// DownloadPhoto downloads the image at rawURL and decodes it, refusing one
// whose shorter side is under PhotoMinShortSide. It records nothing, so a
// caller can try another image before opening a transaction.
func (c *PhotoCache) DownloadPhoto(ctx context.Context, rawURL string) (DownloadedPhoto, error) {
	page, err := c.images.FetchImage(ctx, rawURL)
	if err != nil {
		return DownloadedPhoto{}, err
	}
	img, err := decodePhoto(page.Body)
	if err != nil {
		return DownloadedPhoto{}, fmt.Errorf("%w: %s", err, page.URL)
	}
	digest := sha256.Sum256(page.Body)
	return DownloadedPhoto{url: page.URL, sha256: hex.EncodeToString(digest[:]), img: img}, nil
}

// CachePhoto records p in tx, the caller's transaction, so its photos row
// commits or rolls back with the rows that refer to it. An image whose
// bytes are already recorded returns that row and created false. Otherwise
// p is scaled so its long side is each of PhotoSizes, encoded as JPEG and
// recorded, and created is true: if tx then does not commit, the caller
// removes the files with RemovePhotoFiles. The row's width and height are
// the original image's, and its local_path is the largest size's file
// relative to the data directory.
//
// The store's writer has one connection, so transactions run one at a
// time and an image is never recorded twice.
func (c *PhotoCache) CachePhoto(ctx context.Context, tx *sql.Tx, p DownloadedPhoto) (photo db.Photo, created bool, err error) {
	q := db.New(tx)
	existing, err := q.GetPhotoBySHA256(ctx, p.sha256)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return db.Photo{}, false, fmt.Errorf("recipes: look up photo: %w", err)
	}

	id := store.NewID()
	for _, size := range PhotoSizes {
		if err := writeJPEG(c.path(id, size), scaleLongSide(p.img, size)); err != nil {
			return db.Photo{}, false, errors.Join(err, c.RemovePhotoFiles(id))
		}
	}

	now := store.Now().String()
	bounds := p.img.Bounds()
	photo, err = q.CreatePhoto(ctx, db.CreatePhotoParams{
		ID:        id,
		SourceUrl: p.url,
		LocalPath: path.Join(PhotoDir, photoFileName(id, PhotoSizes[0])),
		Width:     int64(bounds.Dx()),
		Height:    int64(bounds.Dy()),
		Sha256:    p.sha256,
		FetchedAt: now,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		return db.Photo{}, false, errors.Join(fmt.Errorf("recipes: record photo: %w", err), c.RemovePhotoFiles(id))
	}
	return photo, true, nil
}

// RemovePhotoFiles removes the files of photo id at every PhotoSizes
// length; a file already absent is not an error.
func (c *PhotoCache) RemovePhotoFiles(id string) error {
	var errs []error
	for _, size := range PhotoSizes {
		if err := os.Remove(c.path(id, size)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// OpenPhoto opens the stored file of photo id at size. An id that is not a
// canonical ULID, a size not in PhotoSizes, an id with no photos row, or a
// photo whose file is not stored is an error matching fs.ErrNotExist.
func (c *PhotoCache) OpenPhoto(ctx context.Context, id string, size int) (*os.File, error) {
	parsed, err := ulid.ParseStrict(id)
	if err != nil || parsed.String() != id || !slices.Contains(PhotoSizes, size) {
		return nil, fmt.Errorf("recipes: photo %q at size %d: %w", id, size, fs.ErrNotExist)
	}
	if _, err := db.New(c.store.Reader).GetPhoto(ctx, id); errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("recipes: photo %q: %w", id, fs.ErrNotExist)
	} else if err != nil {
		return nil, fmt.Errorf("recipes: look up photo %q: %w", id, err)
	}
	return os.Open(c.path(id, size))
}

func (c *PhotoCache) path(id string, size int) string {
	return filepath.Join(c.dir, photoFileName(id, size))
}

func photoFileName(id string, size int) string {
	return id + "-" + strconv.Itoa(size) + ".jpg"
}

// decodePhoto decodes a JPEG, PNG, WebP or GIF image, checking its pixel
// count from the header before decoding the pixels, and the shorter side of
// the decoded image: a GIF's first frame may be smaller than its screen.
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
	img, err := decode(b)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPhotoFormat, err)
	}
	if size := img.Bounds().Size(); min(size.X, size.Y) < PhotoMinShortSide {
		return nil, fmt.Errorf("%w: %dx%d, shorter side under %d px", ErrPhotoTooSmall, size.X, size.Y, PhotoMinShortSide)
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
