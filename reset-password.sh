#!/usr/bin/env bash
# Password is read from stdin by the Go utility; it is never passed in argv.
set -euo pipefail
cd "$(dirname "$0")"
go run ./cmd/reset-password "$@"
