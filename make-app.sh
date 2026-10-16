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

# Ad-hoc sign with a stable identifier so macOS doesn't re-ask for
# Accessibility/Automation permissions on every rebuild.
codesign --force --deep --sign - --identifier com.switchboard.app "$APP"

# Nudge Launch Services to notice it immediately instead of waiting for its
# own periodic rescan.
/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$APP"

echo "Installed: $APP"
echo "Open Launchpad or Spotlight and search \"Switchboard\"."
