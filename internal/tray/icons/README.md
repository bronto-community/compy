# compy menu-bar icons

The four `compy-menubar-*.svg` files and `HANDOFF.md` are the design owner's
handoff (direction 1c "Track + signals") — see `HANDOFF.md` for the spec:
template-image rules, geometry, and why each state is a shape, not a colour.

The rasters are generated from the SVGs; committed, not rebuilt at build time:

- `{running,stopped,attention}-16.png` / `-32.png` — rendered with headless
  Chrome (`Page.captureScreenshot`, transparent default background, exact
  16/32px viewport). The 16px running raster comes from the pixel-fit
  `compy-menubar-running-16.svg`; every other raster from its 32×32 source.
  Verified black-on-transparent: transparent corners, full-alpha glyph,
  no non-black opaque pixels.
- `{running,stopped,attention}.icns` — `iconutil -c icns` over an iconset of
  `icon_16x16.png` + `icon_16x16@2x.png` (the 16 and 32 PNGs). These are what
  `icons.go` embeds and `systray.SetTemplateIcon` ships to NSImage, which
  keeps both reps and picks 1×/2× per display.

To regenerate after an SVG change: render the PNGs at exactly 16 and 32 px
with any rasterizer that preserves alpha, then repackage:

    mkdir compy-X.iconset
    cp X-16.png compy-X.iconset/icon_16x16.png
    cp X-32.png compy-X.iconset/icon_16x16@2x.png
    iconutil -c icns compy-X.iconset -o X.icns
