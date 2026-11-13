#!/bin/sh
# Regenerates docs/ (docs.go, swagger.json, swagger.yaml) from the
# handlers' annotations. Commit the result; check.sh diffs a fresh
# generation against it.
set -eu
cd "$(dirname "$0")"
swag init -g cmd/app/main.go
