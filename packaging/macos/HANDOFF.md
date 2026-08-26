# compy app icon — design handoff (app-icon section)

icon is **not** a template image, so it carries colour; otherwise the geometry is
identical to the menu-bar glyph.

- The glyph occupies 66% of the plate and sits 1.5% above true centre, so the heavy pad
  does not read as sinking inside the mask.

- Bronto gold `#ECAA0D` on `#101010`; the light variant uses `#8A6631` on `#F7F5EE`
  because gold disappears against paper white.
- Corner radius is 22.37% of the side — Apple's squircle ratio. If you build the icon
  through an asset catalogue with macOS 11+ templates, drop the background rect and let
  the system mask it instead.
- Export the raster set Apple expects: 16, 32, 128, 256, 512 at 1× and 2×. The tail sweep
  survives to 32px; below that only the body mass reads, which is intended.
- The menu-bar SVGs stay black template images — do not ship the coloured app icon into
  the status bar.

## Other directions considered

Discarded, in case the question comes back: a pure fan-in pipeline glyph (clear collector,
no compy), a tail-and-spine abstraction, tapered/clawed footprint variants, and three
anatomically-shaped *Grallator* prints. All are in the design file if you want to revisit.
