# 🏛️ Architecture Decision Records (ADR): Upload & Transcoding Subsystem

This document records the architectural and technology decisions agreed upon for the **Bitmill Video Upload & Transcoding Subsystem**.

---

## ADR-001: Cloudflare R2 for Object Storage

### Status: ACCEPTED
**Date**: 2026-10-27  
**Decision Maker**: User / Solution Architect

### Context:
Video distribution generates significant bandwidth consumption. Traditional cloud providers (AWS S3, Google Cloud Storage) charge substantial outbound egress fees ($0.08 - $0.09 per GB) when serving media to CDN origins or workers.

### Decision:
Use **Cloudflare R2** as the primary Object Storage for both Raw and Processed video assets.
* For local development, **MinIO** is used as an S3-compatible local drop-in emulator.
* Both R2 and MinIO are accessed via the standard **AWS SDK for Go v2** (`github.com/aws/aws-sdk-go-v2/service/s3`), using custom endpoint resolvers (`https://<account_id>.r2.cloudflarestorage.com`).

### Consequences & Trade-offs:
* **Positive**:
  - **Zero data egress fees**: Saves thousands of dollars as video traffic scales.
  - S3 API Compatibility: Presigned URLs (`s3.NewPresignClient`), `PutObject`, and `GetObject` work transparently with the standard AWS Go SDK.
  - Native integration with Cloudflare CDN for edge caching.
* **Negative**:
  - Requires managing Cloudflare API tokens (`R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`) and custom endpoint configurations.

---

## ADR-002: Event-Driven Ingestion via Storage Event Notifications (Option B)

### Status: ACCEPTED
**Date**: 2026-10-27  
**Decision Maker**: User / Solution Architect

### Context:
When a client completes an upload directly to storage via a Presigned URL, the system needs to know when to start transcoding.
* *Alternative A (Client-driven)*: The client makes an HTTP call `POST /api/v1/videos/:id/complete` after finishing its `PUT` upload.
* *Alternative B (Storage Event-driven)*: The storage layer emits an `ObjectCreated` event directly to the backend via Webhook or Queue integration.

### Decision:
Implement **Alternative B (Storage Event-driven Ingestion)**:
1. Cloudflare R2 emits an `ObjectCreated` event via Cloudflare Event Notifications (or MinIO bucket notification webhook in local dev) to the endpoint `POST /api/v1/internal/storage/webhook`.
2. The Upload Service verifies the payload HMAC/token, transitions the video record to `UPLOADED`, and publishes the transcoding job to RabbitMQ.
3. *Fallback support*: The client `/complete` endpoint is maintained as an optional idempotent trigger in case of webhook delays.

### Consequences & Trade-offs:
* **Positive**:
  - **Zero Trust Client**: Eliminates client spoofing or lost signals (e.g., user closes browser right as upload hits 100%).
  - Guaranteed execution: The event only fires when the file is genuinely received and committed by storage.
* **Negative**:
  - Requires public webhook tunneling (e.g., ngrok) or MinIO webhook configuration during local testing.

---

## ADR-003: Multi-Bitrate Adaptive Bitrate (ABR) HLS Packaging

### Status: ACCEPTED
**Date**: 2026-10-27  
**Decision Maker**: User / Solution Architect

### Context:
Viewers stream video across heterogeneous network environments (mobile 4G/5G, unstable Wi-Fi, gigabit fiber). Delivering a single high-bitrate stream causes buffering, while a low-bitrate stream degrades visual quality on desktop displays.

### Decision:
Implement full **Adaptive Bitrate Streaming (ABR)** packaging using Docker FFmpeg to output:
* **Master Playlist (`master.m3u8`)**: Contains `#EXT-X-STREAM-INF` tags referencing all available renditions.
* **4 Resolution Profiles**:
  1. `1080p` (Full HD): 1920x1080 @ 4500 kbps video, 192 kbps audio.
  2. `720p` (HD): 1280x720 @ 2500 kbps video, 128 kbps audio.
  3. `480p` (SD): 854x480 @ 1200 kbps video, 96 kbps audio.
  4. `360p` (Low): 640x360 @ 600 kbps video, 64 kbps audio.
* **Keyframe Alignment**: Enforce identical closed GOP size (`-g 60 -keyint_min 60 -sc_threshold 0`) and fixed segment duration (`-hls_time 4`) across all renditions to allow seamless video player switching.

### Consequences & Trade-offs:
* **Positive**:
  - World-class streaming UX matching YouTube/Twitch standards with automatic resolution switching.
* **Negative**:
  - Higher CPU transcoding load per video (~3-4x compared to single rendition).
  - More storage segments generated per video (~4x file count in Processed R2).

---

## ADR-004: RabbitMQ with Dead Letter Exchange (DLX) as Central Message Broker

### Status: ACCEPTED
**Date**: 2026-10-27  
**Decision Maker**: User / Solution Architect

### Context:
Transcoding must be isolated from API servers. We require a robust message broker supporting explicit message acknowledgement (ACK/NACK), channel multiplexing, and dead-letter routing for failed jobs.

### Decision:
Adopt **RabbitMQ** (AMQP 0-9-1) with the following topology:
1. **Primary Exchange**: `bitmill.video.events` (Topic Exchange).
2. **Main Work Queue**: `video.transcoding.jobs` (Durable, bound to `video.transcode.job`).
3. **Dead Letter Exchange (DLX)**: `bitmill.video.dlx` (Fanout/Direct).
4. **Dead Letter Queue (DLQ)**: `video.transcoding.dlq` (Durable).
5. **Arguments**: `x-dead-letter-exchange: bitmill.video.dlx`, `x-delivery-limit: 3`.

### Consequences & Trade-offs:
* **Positive**:
  - Full AMQP protocol support: Manual ACK prevents task loss if a worker crashes mid-transcode.
  - RabbitMQ Management Dashboard (`:15672`) provides real-time visibility into queue depth, consumption rate, and DLQ messages.
  - Poison pills are cleanly quarantined in the DLQ without starving healthy tasks.
* **Negative**:
  - RabbitMQ requires slightly higher baseline memory (~150MB Erlang VM) than Redis Streams.
