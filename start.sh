#!/bin/bash
cd /Volumes/Proj/workspace/smart-router
# Export vars from .env and .dev.vars so wrangler passes them to the worker
set -a
source .env 2>/dev/null || true
source .dev.vars 2>/dev/null || true
set +a
exec npx wrangler dev --port=8790
