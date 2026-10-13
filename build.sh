#!/bin/bash
# Builds Switchboard for this Mac (or Linux). Needs Go: brew install go
set -e
cd "$(dirname "$0")"
if ! command -v go >/dev/null 2>&1; then
  echo "Go isn't installed. Install it with:  brew install go"
  exit 1
fi
if [ "$(uname)" = "Darwin" ] && ! xcode-select -p >/dev/null 2>&1; then
  echo "Apple's command line tools are needed. Install them with:  xcode-select --install"
  exit 1
fi
echo "Building Switchboard..."
go build -trimpath -ldflags "-s -w" -o switchboard .
echo "Done: ./switchboard"
echo "Next: ./switchboard install   then   ./switchboard start"
