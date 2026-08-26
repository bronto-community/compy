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

## Menu-item indicator icons (`item-*`)

`item-active.svg` (filled dot — the glyph's pad motif), `item-down.svg` /
`item-up.svg` (chevrons, stroke 3 + round caps like the running glyph), and
`item-blank.svg` (fully transparent) are the per-menu-item three-state
indicators on config rows and preset items: active / going down / going up
during an activation swap, blank otherwise. They replaced the native
checkmark as the state carrier. The blank exists because systray has no way
to clear a menu item's icon once set, and painting it on every row keeps
titles aligned. Same pipeline as the menu-bar states: 16 + 32 px
black-on-transparent PNGs via headless Chrome (rendered through a scaling
`<img>` wrapper — a bare SVG navigation renders at natural size and crops;
packaging/macos/README.md lesson), packed per state into `item-*.icns`
(`icon_16x16` + `icon_16x16@2x`), embedded by `icons.go`, shipped through
per-item `MenuItem.SetTemplateIcon` (verified: same `NSImage initWithData`
path as the status icon, so .icns works per-item too — including the
all-transparent blank, which `iconutil` accepts).

To regenerate after an SVG change: render the PNGs at exactly 16 and 32 px
with any rasterizer that preserves alpha, then repackage:

    mkdir compy-X.iconset
    cp X-16.png compy-X.iconset/icon_16x16.png
    cp X-32.png compy-X.iconset/icon_16x16@2x.png
    iconutil -c icns compy-X.iconset -o X.icns
