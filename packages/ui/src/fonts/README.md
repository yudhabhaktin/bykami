# The typeface

`geist-latin.woff2` — Geist, variable, weights 100–900, latin subset only.
29KB. Licensed under the SIL Open Font License 1.1; `OFL.txt` beside it is the
copy that licence requires travel with the file.

Latin alone is deliberate. Google serves Geist in five subsets, and the other
four are dead weight here: Indonesian is written in unaccented ASCII, and the
only non-ASCII character anywhere in `packages/content` or the four sites is the
em dash, which is U+2014 and therefore already inside the latin range. Adding
latin-ext would be another 16KB for glyphs nothing on these sites can render.

It is one variable file rather than the two static instances Google's CSS hands
out for 400 and 600, because the design system asks for both and a second static
face costs more than the axis does.

The `@font-face` lives in `tokens.css`, next to the `--font-sans` that names it,
and points at this file with a relative URL. That is what makes it work: Vite
resolves the URL when it processes the imported stylesheet, hashes the file into
`_astro/`, and rewrites the reference — in `astro dev` as well as in a build.

The previous rule did not work. It sat in `BaseLayout.astro` as a literal
`/fonts/plus-jakarta-sans-variable.woff2` and its comment said the files were
"fetched by `pnpm fonts` into each site's public/fonts". There is no `fonts`
script in any `package.json` in this workspace and no `public/fonts` directory
in any of the four sites, so that URL had always 404'd and every page had always
rendered in the fallback system sans. Nothing regressed when the family changed,
because the family had never been arriving.

Re-fetching, should the subset or the version ever need to move:

```bash
curl -H "User-Agent: Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) \
  AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36" \
  "https://fonts.googleapis.com/css2?family=Geist:wght@100..900&display=swap"
```

The browser User-Agent is load-bearing — without it Google returns TTF for the
whole family instead of the woff2 subsets. Take the `src` URL from the `/* latin
*/` block of the response.
