#!/bin/bash
# Packages Switchboard as a real .app bundle so it shows up in Launchpad,
# Spotlight and Finder, instead of only being runnable from a terminal.
set -e
cd "$(dirname "$0")"
./build.sh

APP="$HOME/Applications/Switchboard.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS"
cp switchboard "$APP/Contents/MacOS/Switchboard"

cat > "$APP/Contents/Info.plist" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>Switchboard</string>
  <key>CFBundleDisplayName</key><string>Switchboard</string>
  <key>CFBundleIdentifier</key><string>com.switchboard.app</string>
  <key>CFBundleExecutable</key><string>Switchboard</string>
  <key>CFBundleVersion</key><string>1</string>
  <key>CFBundleShortVersionString</key><string>2.0.0</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>LSUIElement</key><true/>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
EOF

# Sign with a local self-signed certificate (created once, see the commit
# that added this comment for the setup commands) instead of ad-hoc (-s -).
# ponytail: ad-hoc signing's identity is a hash of the binary's own bytes,
# so it changes on every rebuild and silently invalidates the Accessibility
# grant each time (confirmed 2026-10-19) - a real certificate's identity
# stays constant across rebuilds regardless of code changes, which is what
# actually keeps the grant. Upgrade path if this ever needs to work on
# another Mac: export "Switchboard Local Signing" (with its private key)
# from this Keychain and import it there, or re-run the generation steps.
SIGN_ID="Switchboard Local Signing"
if ! security find-identity -v -p codesigning | grep -q "$SIGN_ID"; then
  echo "No '$SIGN_ID' certificate in your Keychain - falling back to ad-hoc (Accessibility grant won't survive rebuilds)."
  SIGN_ID="-"
fi
codesign --force --deep --sign "$SIGN_ID" --identifier com.switchboard.app "$APP"

# Nudge Launch Services to notice it immediately instead of waiting for its
# own periodic rescan.
/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$APP"

echo "Installed: $APP"
echo "Open Launchpad or Spotlight and search \"Switchboard\"."
