#!/bin/bash
cd /Volumes/Proj/workspace/smart-router
# Export vars from .env
set -a
source .env 2>/dev/null || true
set +a
# Daily backup (keeps last 5 copies)
./scripts/backup.sh 2>/dev/null || true
# Build if needed — check if any .go file is newer than the binary
needs_build=0
if [ ! -f ./smart-router ]; then
  needs_build=1
else
  for f in $(find . -name '*.go' -not -path './vendor/*'); do
    if [ "$f" -nt ./smart-router ]; then
      needs_build=1
      break
    fi
  done
fi
if [ "$needs_build" -eq 1 ]; then
  go build -o smart-router .
fi
exec ./smart-router
