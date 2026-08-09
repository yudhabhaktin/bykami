import { copyFile, writeFile } from "node:fs/promises";

/**
 * The files the icon needs to be reachable at a fixed path. `apple-touch-icon`
 * and `manifest.webmanifest` are conventions, not choices: iOS looks for the
 * first by name when a page has no link tag for it, and the manifest is the
 * only thing that decides what an installed shortcut is called.
 */
const ASSETS = ["icon.svg", "apple-touch-icon.png", "icon-512.png"];

/**
 * One name for every property, including Dimsamcong. The four hostnames are one
 * business — see verticals/root.ts — and a phone home screen shows the name
 * without the page it came from, so four different labels would be four
 * different-looking apps.
 *
 * No `display` and no `start_url`: those turn a marketing site into an
 * installable web app, which is a behaviour nobody asked for. This manifest
 * exists to name the icon and nothing else.
 */
const MANIFEST =
  JSON.stringify(
    {
      name: "by KAMI",
      short_name: "by KAMI",
      icons: [
        { src: "/icon.svg", sizes: "any", type: "image/svg+xml" },
        { src: "/icon-512.png", sizes: "512x512", type: "image/png" },
      ],
    },
    null,
    2,
  ) + "\n";

/**
 * Copies the brand icon into the build output.
 *
 * The assets live once, in `src/brand/`, rather than four times in four
 * `public/` directories. A logo that has to be replaced in a dozen places is a
 * logo that ends up half-replaced — and `tokens.css` is still waiting on the
 * real vector, so this one is going to be replaced.
 *
 * Emitted at `astro:build:done` for the same reason `_headers` is: it is part
 * of what gets deployed, not part of what gets authored. `astro dev` therefore
 * serves no icon, which costs a browser-default globe in a dev tab.
 */
export const brandAssets = () => ({
  name: "bykami:brand-assets",
  hooks: {
    "astro:build:done": async ({ dir, logger }) => {
      await Promise.all(
        ASSETS.map((name) =>
          copyFile(new URL(`brand/${name}`, import.meta.url), new URL(name, dir)),
        ),
      );
      await writeFile(new URL("manifest.webmanifest", dir), MANIFEST, "utf8");
      logger.info(`brand assets written (${ASSETS.length + 1} files)`);
    },
  },
});
