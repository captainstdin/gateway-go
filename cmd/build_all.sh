#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

for dir in "$SCRIPT_DIR"/*/; do
    name=$(basename "$dir")
    echo "Building $name..."
    (cd "$dir" && go build .)
    echo "  -> $name done"
done

echo ""
echo "All binaries built successfully."
