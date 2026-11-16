#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
export GOTOOLCHAIN=local
export GOWORK=off
exec "${GO_BIN:-go}" run -mod=readonly ./tools/protocol install "$@"
