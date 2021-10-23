#!/bin/bash

set -e

# Directory of the script file, independent of where it's called from.
DIR="$(cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd)"
GOCACHE=`go env GOCACHE`
GOMODCACHE=`go env GOMODCACHE`

echo "Building protobuf compiler docker image..."
docker build -t pool-gen-builder \
  .

echo "Compiling and formatting *.proto files..."
docker run \
  --rm \
  --user "$UID:$(id -g)" \
  -e UID=$UID \
  -e GOCACHE=$GOCACHE \
  -e GOMODCACHE=$GOMODCACHE \
  -v "$GOCACHE:$GOCACHE" \
  -v "$GOMODCACHE:$GOMODCACHE" \
  -v "$DIR/../:/build" \
  pool-gen-builder
