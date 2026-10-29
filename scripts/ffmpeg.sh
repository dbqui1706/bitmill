#!/usr/bin/env bash
# Wrapper chạy FFmpeg thông qua Docker, mount thư mục hiện tại vào /work
docker run --rm -i -v "$(pwd):/work" -w /work mwader/static-ffmpeg:latest "$@"
