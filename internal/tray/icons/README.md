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

## Coloured states (opt-in, `tray_colors`)

HANDOFF.md's "never a red dot" is about TEMPLATE images, where colour is
discarded. The `tray_colors` setting (`off` / `errors` / `warnings`, default
`warnings`) colours the icon by `app.Trouble`'s level, as two NON-template
states on top of the shape-only three:

- red — the attention glyph: the collector crashed, is crash-looping, or
  launchd cannot start it (shown even while the process is down; a crash
  must not look like a deliberate stop). With colours `off` it is the
  attention glyph uncoloured.
- yellow — the running glyph (`warnings` mode only): running, but losing
  data, mis-advertised (ports mismatch), or needing a restart. A telemetry
  port other than the configured one is NOT yellow: there is nothing for
  the user to do about it (owner ruling 2026-10-02).

Why the icon is coloured is never left to guesswork: each reason is its own
menu line under the status block (✕ red, ⚠ yellow; clicking one opens
compy) and the icon's hover tooltip lists them all — the same sentences the
web UI sidebar shows.

Collector log lines feed neither: a long-running collector keeps every
transient error in its log, and real damage shows up as a crash or a
rising dropped counter anyway.

They are not committed rasters: `icons.go` tints the committed 16/32 PNGs
at runtime (alpha kept, so the geometry is the designed glyph's) and packs
them as icp4 + ic11 .icns. The plain states stay templates, so they keep
following the menu bar's appearance; `off` is the original design exactly.
