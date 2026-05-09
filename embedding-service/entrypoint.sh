#!/bin/sh
# Populate /models from /models-baked on first start. Lets us bake the
# default model into image layers (fast, deterministic) while still
# allowing a Docker volume to persist runtime-downloaded alternatives.
set -e

if [ -d /models-baked ] && [ ! -f /models/.populated ]; then
    echo "[entrypoint] populating /models from /models-baked..."
    mkdir -p /models
    # cp -rn: don't overwrite if dest already has files (e.g. from a
    # previous container that downloaded a different model).
    cp -rn /models-baked/. /models/ 2>/dev/null || true
    touch /models/.populated
    echo "[entrypoint] /models populated."
else
    echo "[entrypoint] /models already populated, skipping bake copy."
fi

exec "$@"
