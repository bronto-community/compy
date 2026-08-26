# packaging/macos — app icon + compy.app bundle

`compy-appicon-dark.svg` / `compy-appicon-light.svg` are the design owner's
app-icon handoff (spec: `HANDOFF.md`). The dark variant is the shipped one;
the light variant ("for paper-white contexts") is vendored source-only —
asset-catalogue light/dark icon variants need Xcode tooling we don't carry.

`compy.icns` is generated from the dark SVG; committed, not rebuilt at build
time. Pipeline (same headless-Chrome CDP approach as
`internal/tray/icons/README.md`):

- Render the 512×512 SVG at exactly 16, 32, 64, 128, 256, 512, 1024 px
  (`Emulation.setDeviceMetricsOverride` per size,
  `Emulation.setDefaultBackgroundColorOverride` alpha 0 for transparent
  squircle corners, `Page.captureScreenshot` with an exact clip).
- Machine-verified per size: exact dimensions, corner alpha 0, opaque
  centre, both palette colours (#ECAA0D gold, #101010 ground) present.
  (The fixed 512px width/height in the SVG mean the render must go through
  an HTML wrapper that scales an <img> to the target size — a bare data:
  SVG navigation renders at natural size and the screenshot crops.)
- Name them into the Apple iconset (icon_16x16 … icon_512x512@2x, shared
  sizes duplicated), then `iconutil -c icns … -o compy.icns`.

`make-app.sh <path/to/compy>` assembles a `compy.app` next to the binary:
Info.plist (name/display name "compy", id `io.bronto.compy`, icon), the
icns, a symlink to the binary, and a `compy-window` shim as
CFBundleExecutable so `open compy.app` runs `compy window`. macOS derives
app identity (menu name, Dock icon) from the bundle containing the running
executable's path; a symlink inside `Contents/MacOS` is enough — verified
via `lsappinfo` — and never goes stale when the binary is rebuilt. The tray
spawns the window through the bundle automatically when it exists
(`internal/tray` `windowExe`).
