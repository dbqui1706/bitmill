#!/usr/bin/env bash
# Wrapper chạy ffprobe thông qua Docker, mount thư mục hiện tại vào /work
docker run --rm -i --entrypoint /ffprobe -v "$(pwd):/work" -w /work mwader/static-ffmpeg:latest "$@"
