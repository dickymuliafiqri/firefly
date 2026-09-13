#!/usr/bin/env bash
set -e

# Resolve paths relative to this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
FRONTEND_DIR="$ROOT_DIR/frontend"
DIST_INDEX="$FRONTEND_DIR/dist/index.html"

cd "$FRONTEND_DIR"

NEEDS_BUILD=false

if [ ! -f "$DIST_INDEX" ]; then
  NEEDS_BUILD=true
else
  # Check if any frontend source files or configs are newer than dist/index.html
  CHANGED_COUNT=$(find src index.html package.json vite.config.ts tsconfig*.json -type f -newer "$DIST_INDEX" 2>/dev/null | wc -l)
  if [ "$CHANGED_COUNT" -gt 0 ]; then
    NEEDS_BUILD=true
  fi
fi

if [ "$NEEDS_BUILD" = true ]; then
  echo ">> [Air Autobuild] Frontend change detected. Rebuilding frontend assets..."
  if command -v bun >/dev/null 2>&1; then
    bun run build
  elif command -v pnpm >/dev/null 2>&1; then
    pnpm run build
  elif command -v npm >/dev/null 2>&1; then
    npm run build
  else
    echo ">> [Air Autobuild] Error: Neither bun, pnpm, nor npm found in PATH" >&2
    exit 1
  fi
  echo ">> [Air Autobuild] Frontend build complete. Embedded assets ready."
fi
