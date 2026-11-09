#!/usr/bin/env bash
# Stop the service first; build.sh stages frontend and produces the new executable.
set -euo pipefail
cd "$(dirname "$0")"
prtseek_OUTPUT="$PWD/prtseek-next" bash ./build.sh
echo 'Build complete: prtseek-next. Stop prtseek, replace the executable, then restart.'
