# compy app icon — design handoff (app-icon section)

`compy-appicon-dark.svg` (recommended) and `compy-appicon-light.svg`, both 512×512.

An app icon is **not** a template image — it keeps its colour and needs the macOS
squircle, so this is where the actual Compsognathus lives: long tail, horizontal spine,
small head. The menu-bar track recurs at 34% opacity in the lower corner so the two read
as one family without being the same drawing.

- Bronto gold `#ECAA0D` on `#101010`; the light variant uses `#8A6631` on `#F7F5EE`
  because gold disappears against paper white.
- Corner radius is 22.37% of the side — Apple's squircle ratio. If you build the icon
  through an asset catalogue with macOS 11+ templates, drop the background rect and let
  the system mask it instead.
- Export the raster set Apple expects: 16, 32, 128, 256, 512 at 1× and 2×. The tail sweep
  survives to 32px; below that only the body mass reads, which is intended.
- Do not reuse the app icon in the menu bar, or the menu-bar glyph in the Dock. They are
  deliberately different drawings at different densities.
