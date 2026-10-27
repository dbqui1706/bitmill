#!/bin/sh
set -e

echo "Setting up MinIO Buckets..."
docker run --rm --network bitmill_default \
  -e MC_CONFIG_DIR=/tmp/.mc \
  cgr.dev/chainguard/minio-client:latest \
  alias set local_minio http://bitmill-minio:9000 bitmill_admin bitmill_secret_password

# To persist across commands in docker run, we can use a docker volume or run with sh
# But minio-client doesn't have sh.
# Let's see: we can use a named docker volume with proper permissions or create via AWS CLI or Go.
