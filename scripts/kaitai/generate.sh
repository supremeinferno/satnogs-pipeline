#!/usr/bin/env bash
# Generates Go parsers for satellites' radio formats from the Kaitai Struct
# definitions in satnogs-decoders (AGPL-3.0), at the commit pinned below, into
# internal/decoders/<name>/<name>.go. Needs git and node.
#
#   scripts/kaitai/generate.sh catsat snuglite sharjahsat1
#
# The generated files are committed. They are not edited by hand.
set -euo pipefail
cd "$(dirname "$0")/../.."

COMMIT=d4289a41820e1c4b5e8116b02b5490fc448a187d
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

git clone -q https://gitlab.com/librespacefoundation/satnogs/satnogs-decoders.git "$work/decoders"
git -C "$work/decoders" checkout -q "$COMMIT"

(cd scripts/kaitai && npm ci --silent)

for name in "$@"; do
  out=internal/decoders/$name
  node scripts/kaitai/gen.js "$work/decoders/ksy/$name.ksy" "$out"
  gofmt -w "$out"
done
