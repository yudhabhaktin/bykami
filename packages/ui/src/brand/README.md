# The brand assets

Two things live here, and they are not the same artwork:

- `logo.png` — the full "studio by KAMI" lockup, used where there is room to
  read a wordmark. Trimmed to its own ink and resized from the 4884px master;
  stored as greyscale-plus-alpha rather than RGBA, because the ink is one flat
  black and only the coverage varies, which is a third off the file for a
  worst-case channel error of 4/255.
- `icon.svg` — the camera mark from that lockup, alone, on a dark tile. The app
  icon. The file's own comment says why it is measured rectangles rather than a
  trace, and which of its oddities are in the master artwork.

`icon.svg` is the source for the two PNGs beside it. They are committed because
the surfaces that need them cannot rasterise anything: iOS ignores SVG for
`apple-touch-icon`, and a web app manifest wants a raster icon for the Android
home screen.

Regenerating them needs a rasteriser, which is deliberately not a dependency of
this workspace — it would be a native build on every `pnpm install` for two
files that change when the logo does, which is roughly never:

```bash
brew install librsvg
cd packages/ui/src/brand
rsvg-convert -w 180 -h 180 icon.svg -o apple-touch-icon.png
rsvg-convert -w 512 -h 512 icon.svg -o icon-512.png
```

`brand.mjs` copies the icon and its two PNGs into each site's `dist/` at build
time, so the four Astro sites hold no copy of their own. It does not copy
`logo.png`: no site asks for the lockup yet, and an asset shipped to four
`dist/` directories that nothing references is four downloads nobody wanted.

Three surfaces do hold copies, and all three are deliberate:

- `apps/kiosk/public/` — `icon.svg`, `apple-touch-icon.png` and `logo.png`. The
  kiosk bundle is embedded in the Go agent and must build without the Astro
  workspace, which is the same reason it restates the design tokens instead of
  importing `tokens.css`.
- `agent/internal/httpd` serves the customer's download page and takes no copy:
  it reads `dist/logo.png` out of the kiosk bundle it already embeds, at
  `/brand/logo.png`. That path is exempt from the booth's access token — see
  the guard in `httpd.go` — because the person loading it is a customer holding
  only a gallery link.
- `api/internal/admin/` — `logo.png` and `icon.svg`, embedded and served as
  routes. The console is Go with no static asset pipeline, and its CSP names
  `img-src 'self'` with no `data:`, so an inlined URI would be refused.

Change the mark and those copies have to follow. `logo.png` is byte-identical in
all three places; `shasum` across them is the check.
