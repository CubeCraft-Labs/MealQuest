"""Generates the PWA icons in web/public from the MealQuest logo.

Run from the repository root with Pillow installed:

    python3 web/scripts/generate-icons.py

The logo fills the regular icons edge to edge. The maskable icon shrinks it
onto the colour of its own parchment, so the banner stays inside
the circular safe zone (radius 40% of the icon) that launchers may crop to.
"""

from pathlib import Path

from PIL import Image, ImageDraw, ImageFilter, ImageStat

ROOT = Path(__file__).resolve().parents[2]
SOURCE = ROOT / "design" / "logos" / "logo_v1_2Oct2026.jpg"
PUBLIC = ROOT / "web" / "public"

MASKABLE_SCALE = 0.66


def resized(logo: Image.Image, size: int) -> Image.Image:
    return logo.resize((size, size), Image.Resampling.LANCZOS)


def maskable(logo: Image.Image, size: int) -> Image.Image:
    # The padding is the median colour of the logo's top edge, which is bare
    # parchment; a feathered edge hides the seam.
    top_edge = ImageStat.Stat(logo.crop((0, 0, logo.width, logo.height // 40)))
    canvas = Image.new("RGB", (size, size), tuple(round(c) for c in top_edge.median))
    inner = round(size * MASKABLE_SCALE)
    offset = (size - inner) // 2
    feather = Image.new("L", (inner, inner), 0)
    margin = inner // 24
    ImageDraw.Draw(feather).rectangle((margin, margin, inner - margin, inner - margin), fill=255)
    feather = feather.filter(ImageFilter.GaussianBlur(margin / 2))
    canvas.paste(resized(logo, inner), (offset, offset), feather)
    return canvas


def main() -> None:
    logo = Image.open(SOURCE).convert("RGB")
    if logo.width != logo.height:
        raise SystemExit(f"{SOURCE} is {logo.width}x{logo.height}; the logo must be square")
    outputs = {
        PUBLIC / "icons" / "icon-192.png": resized(logo, 192),
        PUBLIC / "icons" / "icon-512.png": resized(logo, 512),
        PUBLIC / "icons" / "icon-maskable-512.png": maskable(logo, 512),
        PUBLIC / "apple-touch-icon.png": resized(logo, 180),
    }
    for path, image in outputs.items():
        path.parent.mkdir(parents=True, exist_ok=True)
        image.save(path, optimize=True)
        print(f"wrote {path.relative_to(ROOT)} ({image.width}x{image.height})")


if __name__ == "__main__":
    main()
