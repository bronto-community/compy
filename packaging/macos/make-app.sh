#!/bin/sh
# make-app.sh <path/to/compy-binary> [version] — assemble a compy.app next to
# the given binary, so macOS shows the right identity for the standalone
# window (`compy window`): app-menu name "compy" and the dino Dock icon
# instead of the generic name of a bare executable. AppKit derives that
# identity from the bundle containing the running executable's path, so the
# bundle holds a symlink to the binary (identity survives the symlink —
# verified with lsappinfo — and a symlink never goes stale on rebuild) plus
# a shim as CFBundleExecutable so `open compy.app` runs `compy window`.
#
# The tray prefers this bundle automatically when it sits next to the compy
# binary it is running from (internal/tray windowExe).
set -eu

bin=${1:?usage: make-app.sh path/to/compy [version]}
bin=$(cd "$(dirname "$bin")" && pwd)/$(basename "$bin")
# CFBundleShortVersionString and CFBundleVersion must be numeric x[.y[.z]],
# so keep only the leading numeric part: release versions ("0.1.3") pass
# through, snapshot ones ("0.1.3-SNAPSHOT-509ed1a") lose the suffix. The
# argument is optional so the dev-loop invocation in CONTRIBUTING.md still
# works; unversioned bundles get 0.0.0 rather than a stale hardcoded number.
short=$(printf '%s' "${2:-}" | sed -n 's/^\([0-9][0-9.]*\).*/\1/p')
[ -n "$short" ] || short=0.0.0
[ -x "$bin" ] || { echo "not an executable: $bin" >&2; exit 1; }
here=$(cd "$(dirname "$0")" && pwd)
app=$(dirname "$bin")/compy.app

rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$here/compy.icns" "$app/Contents/Resources/compy.icns"
ln -s "../../../$(basename "$bin")" "$app/Contents/MacOS/compy"

cat > "$app/Contents/MacOS/compy-window" <<'EOF'
#!/bin/sh
exec "$(dirname "$0")/compy" window "$@"
EOF
chmod +x "$app/Contents/MacOS/compy-window"

cat > "$app/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key><string>compy</string>
	<key>CFBundleDisplayName</key><string>compy</string>
	<key>CFBundleIdentifier</key><string>io.bronto.compy</string>
	<key>CFBundleExecutable</key><string>compy-window</string>
	<key>CFBundleIconFile</key><string>compy.icns</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>$short</string>
	<key>CFBundleVersion</key><string>$short</string>
	<key>LSMinimumSystemVersion</key><string>11.0</string>
	<key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
EOF

echo "assembled $app"
