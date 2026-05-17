#!/bin/bash
cd /Volumes/Proj/workspace/smart-router
# Export vars from .env
set -a
source .env 2>/dev/null || true
set +a
# Build if needed
if [ ! -f ./smart-router ] || [ main.go -nt ./smart-router ]; then
  go build -o smart-router .
fi
exec ./smart-router
