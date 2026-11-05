#!/bin/sh
set -eu
here=$(cd "$(dirname "$0")" && pwd)
bin="$HOME/.local/bin/tabs"
mkdir -p "$(dirname "$bin")"
go -C "$here" build -o "$bin" .
"$bin" install --extension "$here/extension"
