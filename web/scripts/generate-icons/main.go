// Command generate-icons writes the PWA icons and favicons in web/public from
// the MealQuest logo. Run it from the repository root:
//
//	go run ./web/scripts/generate-icons
//
// The logo fills the regular icons edge to edge. The maskable icon shrinks it
// onto the colour of its own parchment, so the banner stays inside the
// circular safe zone (radius 40% of the icon) that launchers may crop to.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"

	"golang.org/x/image/draw"
)

const (
	source        = "design/logos/logo_v1_2Oct2026.jpg"
	public        = "web/public"
	maskableScale = 0.66
)

// faviconSizes are the images packed into favicon.ico.
var faviconSizes = []int{16, 32, 48}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "generate-icons:", err)
		os.Exit(1)
	}
}

func run() error {
	logo, err := load(source)
	if err != nil {
		return err
	}
	if b := logo.Bounds(); b.Dx() != b.Dy() {
		return fmt.Errorf("%s is %dx%d; the logo must be square", source, b.Dx(), b.Dy())
	}
	outputs := []struct {
		path  string
		image *image.RGBA
	}{
		{"icons/icon-192.png", resized(logo, 192)},
		{"icons/icon-512.png", resized(logo, 512)},
		{"icons/icon-maskable-512.png", maskable(logo, 512)},
		{"icons/favicon-16.png", resized(logo, 16)},
		{"icons/favicon-32.png", resized(logo, 32)},
		{"apple-touch-icon.png", resized(logo, 180)},
	}
	for _, out := range outputs {
		path := filepath.Join(public, out.path)
		if err := save(path, out.image); err != nil {
			return err
		}
		fmt.Printf("wrote %s (%dx%d)\n", path, out.image.Rect.Dx(), out.image.Rect.Dy())
	}
	ico, err := favicon(logo)
	if err != nil {
		return err
	}
	path := filepath.Join(public, "favicon.ico")
	if err := os.WriteFile(path, ico, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%v)\n", path, faviconSizes)
	return nil
}

// favicon is an ICO file holding the logo at each of faviconSizes as an
// embedded PNG: a 6-byte header, a 16-byte directory entry per image, then
// the images.
func favicon(logo image.Image) ([]byte, error) {
	var images [][]byte
	for _, size := range faviconSizes {
		var buf bytes.Buffer
		if err := encode(&buf, resized(logo, size)); err != nil {
			return nil, fmt.Errorf("encode favicon %dx%d: %w", size, size, err)
		}
		images = append(images, buf.Bytes())
	}
	var out bytes.Buffer
	le := binary.LittleEndian
	out.Write(le.AppendUint16(nil, 0))                   // reserved
	out.Write(le.AppendUint16(nil, 1))                   // type: icon
	out.Write(le.AppendUint16(nil, uint16(len(images)))) // image count
	offset := 6 + 16*len(images)
	for i, size := range faviconSizes {
		out.WriteByte(byte(size))           // width
		out.WriteByte(byte(size))           // height
		out.WriteByte(0)                    // palette size: none
		out.WriteByte(0)                    // reserved
		out.Write(le.AppendUint16(nil, 1))  // colour planes
		out.Write(le.AppendUint16(nil, 32)) // bits per pixel
		out.Write(le.AppendUint32(nil, uint32(len(images[i]))))
		out.Write(le.AppendUint32(nil, uint32(offset)))
		offset += len(images[i])
	}
	for _, img := range images {
		out.Write(img)
	}
	return out.Bytes(), nil
}

// load decodes the logo. The file is a PNG despite its .jpg name, so the
// format is sniffed rather than taken from the extension.
func load(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return img, nil
}

func save(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := encode(f, img); err != nil {
		f.Close()
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return f.Close()
}

func encode(w io.Writer, img image.Image) error {
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	return enc.Encode(w, img)
}

func resized(logo image.Image, size int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.CatmullRom.Scale(dst, dst.Rect, logo, logo.Bounds(), draw.Src, nil)
	return dst
}

func maskable(logo image.Image, size int) *image.RGBA {
	// The padding is the median colour of the logo's top edge, which is bare
	// parchment; a feathered edge hides the seam.
	b := logo.Bounds()
	canvas := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(canvas, canvas.Rect, image.NewUniform(medianColour(logo, image.Rect(b.Min.X, b.Min.Y, b.Max.X, b.Min.Y+b.Dy()/40))), image.Point{}, draw.Src)
	inner := int(math.Round(float64(size) * maskableScale))
	offset := (size - inner) / 2
	margin := inner / 24
	at := image.Rect(offset, offset, offset+inner, offset+inner)
	draw.DrawMask(canvas, at, resized(logo, inner), image.Point{}, feather(inner, margin), image.Point{}, draw.Over)
	return canvas
}

// medianColour is the per-channel median of the pixels in r.
func medianColour(img image.Image, r image.Rectangle) color.RGBA {
	var channels [3][]uint8
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := color.RGBAModel.Convert(img.At(x, y)).(color.RGBA)
			channels[0] = append(channels[0], c.R)
			channels[1] = append(channels[1], c.G)
			channels[2] = append(channels[2], c.B)
		}
	}
	var median [3]uint8
	for i, values := range channels {
		slices.Sort(values)
		median[i] = values[len(values)/2]
	}
	return color.RGBA{R: median[0], G: median[1], B: median[2], A: 0xff}
}

// feather is an opaque square inset by margin on a size×size mask, its edge
// softened by a Gaussian blur with a standard deviation of margin/2.
func feather(size, margin int) *image.Alpha {
	values := make([]float64, size*size)
	for y := margin; y <= size-margin && y < size; y++ {
		for x := margin; x <= size-margin && x < size; x++ {
			values[y*size+x] = 255
		}
	}
	kernel := gaussian(float64(margin) / 2)
	values = blur(values, size, kernel, 1, size)
	values = blur(values, size, kernel, size, 1)
	mask := image.NewAlpha(image.Rect(0, 0, size, size))
	for i, v := range values {
		mask.Pix[i] = uint8(math.Round(min(max(v, 0), 255)))
	}
	return mask
}

// gaussian is a normalised kernel reaching three standard deviations.
func gaussian(sigma float64) []float64 {
	radius := int(math.Ceil(3 * sigma))
	kernel := make([]float64, 2*radius+1)
	var sum float64
	for i := range kernel {
		d := float64(i - radius)
		kernel[i] = math.Exp(-d * d / (2 * sigma * sigma))
		sum += kernel[i]
	}
	for i := range kernel {
		kernel[i] /= sum
	}
	return kernel
}

// blur convolves each line of a size×size grid with kernel: step is the
// distance between neighbours along a line and stride between lines. Samples
// beyond the edge repeat the edge.
func blur(values []float64, size int, kernel []float64, step, stride int) []float64 {
	radius := len(kernel) / 2
	out := make([]float64, len(values))
	for line := range size {
		for i := range size {
			var v float64
			for k, w := range kernel {
				j := min(max(i+k-radius, 0), size-1)
				v += w * values[line*stride+j*step]
			}
			out[line*stride+i*step] = v
		}
	}
	return out
}
