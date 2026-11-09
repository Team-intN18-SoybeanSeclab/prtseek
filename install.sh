#!/usr/bin/env bash
# Build this checkout; the application initializes SQLite on first launch.
set -euo pipefail
cd "$(dirname "$0")"
prtseek_OUTPUT="$PWD/prtseek" bash ./build.sh
echo 'Ready. Run: bash start.sh'
