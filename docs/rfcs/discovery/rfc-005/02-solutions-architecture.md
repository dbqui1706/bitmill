# 📐 Solutions Architecture & Clean Architecture Specification: GCP Migration

**Document ID**: `RFC-DISCOVERY-005`  
**Subsystem**: Cloud Storage Adapter, Serverless Transcoder Client, Cloud Pub/Sub Consumer, Clean Architecture Inversion of Control (IoC)  
**Status**: DRAFT (Review Ready)  
**Author**: Principal Solutions & Cloud Architect  
**Target Environment**: Google Cloud Platform (`asia-southeast1` or `us-central1`), Golang 1.24+  
**Target Platform**: Bitmill Media Platform (`go-streaming`)  

---

## 1. System Architecture Overview

```
                                  GOOGLE CLOUD PLATFORM (GCP)
                 ┌─────────────────────────────────────────────────────────────┐
                 │                                                             │
                 │   ┌────────────────────────┐                                │
                 │   │  bitmill-raw-videos    │◄─────────────────┐             │
                 │   │  (GCS Raw Bucket)      │                  │             │
                 │   └───────────┬────────────┘                  │             │
                 │               │                               │             │
                 │               │ Read Source                   │             │
                 │               ▼                               │             │
                 │   ┌────────────────────────┐                  │             │
                 │   │   Cloud Transcoder     │                  │             │
                 │   │   API (Serverless)     │                  │             │
                 │   └───────────┬────────────┘                  │             │
                 │               │                               │             │
                 │               ├────────────────────────┐      │ Direct      │
                 │               │ Write HLS ABR Chunks   │      │ Resumable   │
                 │               ▼                        │      │ Upload      │
                 │   ┌────────────────────────┐           │      │ (Signed URL)│
                 │   │bitmill-processed-videos│           │      │             │
                 │   │(GCS Public HLS Bucket) │           │      │             │
                 │   └───────────┬────────────┘           │      │             │
                 │               │                        │      │             │
                 │               │ Stream HLS             │      │             │
                 │               ▼                        │      │             │
                 │         [HLS Viewers]                  │      │             │
                 │                                        │      │             │
                 │   ┌────────────────────────┐           │      │             │
                 │   │     Cloud Pub/Sub      │◄──────────┘      │             │
                 │   │   (Job Status Topic)   │ Pub/Sub Event    │             │
                 │   └───────────┬────────────┘                  │             │
                 │               │                               │             │
                 └───────────────┼───────────────────────────────┼─────────────┘
                                 │ Push Webhook / Pull           │
                                 ▼                               │
                 ┌───────────────────────────────────────────────┴─────────────┐
                 │                   BITMILL BACKEND (VPS)                     │
                 │                                                             │
                 │   ┌─────────────────────────────────────────────────────┐   │
                 │   │              Go API Server (Gin Framework)          │   │
                 │   │                                                     │   │
                 │   │   • Video Upload Service (Issues GCS Signed URLs)   │   │
                 │   │   • GCP Pub/Sub Webhook Handler (Marks Video READY) │   │
                 │   │   • Rendition Metadata Synchronizer                 │   │
                 │   └──────────────────────────┬──────────────────────────┘   │
                 │                              │                              │
                 │                              ▼                              │
                 │   ┌─────────────────────────────────────────────────────┐   │
                 │   │          PostgreSQL 15 (Video & Job States)         │   │
                 │   └─────────────────────────────────────────────────────┘   │
                 └─────────────────────────────────────────────────────────────┘
```

---

## 2. Inversion of Control (IoC) & Clean Architecture Mapping

The migration preserves strict architectural boundaries in `go-streaming/internal/`. Domain contracts remain 100% agnostic to Google Cloud SDKs.

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                          DOMAIN LAYER (0 Dependencies)                      │
│                                                                             │
│   type StorageProvider interface {                                          │
│       GeneratePresignedPutURL(...) (string, error)                          │
│       CreateMultipartUpload(...) (string, error)                            │
│       GetDownloadURL(...) (string, error)                                   │
│       CheckObjectExists(...) (bool, error)                                  │
│       ...                                                                   │
│   }                                                                         │
│                                                                             │
│   type TranscoderJobDispatcher interface {                                  │
│       SubmitJob(ctx context.Context, job TranscodeJobRequest) (string, error│
│       GetJobStatus(ctx context.Context, jobID string) (*JobStatus, error)   │
│   }                                                                         │
└──────────────────────────────────────▲──────────────────────────────────────┘
                                       │ Implements
┌──────────────────────────────────────┴──────────────────────────────────────┐
│                    INFRASTRUCTURE LAYER (Concrete Adapters)                 │
│                                                                             │
│   package storage                                                           │
│   type GCSStorageAdapter struct {                                           │
│       client *storage.Client                                                │
│       signer *googleAuth.Signer (or Service Account Private Key)            │
│   }                                                                         │
│   var _ video.StorageProvider = (*GCSStorageAdapter)(nil)                   │
│                                                                             │
│   package transcoder                                                        │
│   type GCPCloudTranscoderAdapter struct {                                   │
│       client *transcoder.Service                                            │
│       projectID string                                                      │
│       region    string                                                      │
│   }                                                                         │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 3. Storage Provider Design: Google Cloud Storage (GCS)

### 3.1 Bucket Architecture (`bitmill-raw-videos` & `bitmill-processed-videos`)
Following `@skill:google-cloud-storage-bucket-architect` and `@skill:gcs-security-assessment`:

| Parameter | `bitmill-raw-videos` | `bitmill-processed-videos` |
| :--- | :--- | :--- |
| **Location / Region** | `asia-southeast1` (Singapore) or `us-central1` | Same as raw bucket (Zero inter-region egress) |
| **Default Storage Class** | Standard | Standard |
| **Access Control** | **Uniform Bucket-Level Access (UBLA)** | **Uniform Bucket-Level Access (UBLA)** |
| **Public Access** | Enforce Public Access Prevention (`inherited`/`enforced`) | Public Read Allowed (`allUsers` has `Storage Object Viewer`) |
| **Lifecycle Policy** | Delete objects after 7 days (`age: 7`) | None (Permanent HLS stream retention) |
| **CORS Policy** | Allowed Origins: creator upload domains<br>Methods: `PUT`, `POST`, `OPTIONS`<br>Headers: `Content-Type`, `x-goog-*`<br>MaxAge: 3600 | Allowed Origins: `*`<br>Methods: `GET`, `HEAD`, `OPTIONS`<br>Headers: `*`<br>Exposed: `Content-Range`, `Content-Length`, `ETag`<br>MaxAge: 3600 |

### 3.2 Resumable V4 Signed URL Generation
For raw video ingestion:
1. Client requests an upload session via `POST /api/v1/videos/upload/resumable`.
2. Bitmill API verifies authentication and generates a Google Cloud Storage Signed URL using V4 signing with the Service Account private key (`credentials.json`):
   ```go
   opts := &storage.SignedURLOptions{
       GoogleAccessID: saEmail,
       PrivateKey:     saPrivateKey,
       Method:         http.MethodPut,
       Expires:        time.Now().Add(15 * time.Minute),
       ContentType:    "video/mp4",
       Scheme:         storage.SigningSchemeV4,
   }
   url, err := storage.SignedURL(bucketName, objectKey, opts)
   ```
3. Client executes a single streaming HTTP PUT or GCS Resumable Upload directly to Google Cloud without proxying through VPS memory.

---

## 4. Serverless Transcoder Architecture: Google Cloud Transcoder API

### 4.1 ABR Ladder & Encoding Specification
The Transcoder Job creates an HLS packaging configuration producing 4 elementary video streams and 1 audio stream:

```json
{
  "inputs": [
    {
      "key": "input0",
      "uri": "gs://bitmill-raw-videos/videos/{video_id}/raw.mp4"
    }
  ],
  "editList": [
    {
      "key": "atom0",
      "inputs": ["input0"]
    }
  ],
  "elementaryStreams": [
    {
      "key": "video_1080p",
      "videoStream": {
        "h264": {
          "heightPixels": 1080,
          "widthPixels": 1920,
          "bitrateBps": 4500000,
          "frameRate": 30,
          "rateControlMode": "vbr",
          "preset": "veryfast",
          "profile": "high"
        }
      }
    },
    {
      "key": "video_720p",
      "videoStream": {
        "h264": {
          "heightPixels": 720,
          "widthPixels": 1280,
          "bitrateBps": 2500000,
          "frameRate": 30,
          "rateControlMode": "vbr",
          "preset": "veryfast",
          "profile": "main"
        }
      }
    },
    {
      "key": "video_480p",
      "videoStream": {
        "h264": {
          "heightPixels": 480,
          "widthPixels": 854,
          "bitrateBps": 1000000,
          "frameRate": 30,
          "rateControlMode": "vbr",
          "preset": "veryfast",
          "profile": "main"
        }
      }
    },
    {
      "key": "video_360p",
      "videoStream": {
        "h264": {
          "heightPixels": 360,
          "widthPixels": 640,
          "bitrateBps": 600000,
          "frameRate": 30,
          "rateControlMode": "vbr",
          "preset": "veryfast",
          "profile": "baseline"
        }
      }
    },
    {
      "key": "audio_aac",
      "audioStream": {
        "codec": "aac",
        "bitrateBps": 128000,
        "channelCount": 2,
        "sampleRateHertz": 48000
      }
    }
  ],
  "muxStreams": [
    {
      "key": "hls_1080p",
      "fileName": "1080p.m3u8",
      "container": "ts",
      "elementaryStreams": ["video_1080p", "audio_aac"],
      "segmentSettings": { "segmentDuration": "2s" }
    },
    {
      "key": "hls_720p",
      "fileName": "720p.m3u8",
      "container": "ts",
      "elementaryStreams": ["video_720p", "audio_aac"],
      "segmentSettings": { "segmentDuration": "2s" }
    },
    {
      "key": "hls_480p",
      "fileName": "480p.m3u8",
      "container": "ts",
      "elementaryStreams": ["video_480p", "audio_aac"],
      "segmentSettings": { "segmentDuration": "2s" }
    },
    {
      "key": "hls_360p",
      "fileName": "360p.m3u8",
      "container": "ts",
      "elementaryStreams": ["video_360p", "audio_aac"],
      "segmentSettings": { "segmentDuration": "2s" }
    }
  ],
  "manifests": [
    {
      "fileName": "master.m3u8",
      "type": "HLS",
      "muxStreams": ["hls_1080p", "hls_720p", "hls_480p", "hls_360p"]
    }
  ],
  "pubsubDestination": {
    "topic": "projects/{PROJECT_ID}/topics/bitmill-transcoder-events"
  },
  "outputUri": "gs://bitmill-processed-videos/videos/{video_id}/hls/"
}
```

---

## 5. Event Ingestion Architecture: Cloud Pub/Sub to Bitmill API

### 5.1 Push Webhook vs. Pull Subscriber Trade-Off Matrix

| Evaluation Criterion | Option A: Cloud Pub/Sub Push Webhook | Option B: Cloud Pub/Sub Pull Worker Daemon |
| :--- | :--- | :--- |
| **VPS Resource Footprint** | **Zero persistent background thread**; invokes standard HTTP Gin router endpoint (`POST /api/v1/webhooks/gcp-transcoder`). | Requires persistent Go goroutine pulling messages over gRPC channel. |
| **Firewall / Ingress Requirement**| Requires public HTTPS endpoint reachable from Google Cloud (`devlungtung.htcwood.com` or `api.htcwood.com`). | Works behind NAT or firewall without exposing an inbound HTTP endpoint. |
| **Security & Authentication** | Google Cloud validates using OIDC Bearer Token signed by Google Service Account. | Uses local `credentials.json` with `roles/pubsub.subscriber`. |
| **Delivery Latency** | Sub-second (immediate HTTP POST upon transcode completion). | ~1-2 seconds polling interval. |
| **Local Development Ergonomics** | Requires tunnel (e.g., ngrok/cloudflared) during local testing. | Native support for local development without public IP. |
| **Architectural Verdict** | **Recommended for Production** (Clean, serverless, reactive). | **Recommended as Fallback/Dev Mode** (Local verify scripts). |

---

## 6. Architectural Trade-Off Analysis

```
┌──────────────────────────────────────────────────────────────────────────────────────────────────┐
│                             ARCHITECTURAL DECISION MATRIX (TRADE-OFFS)                           │
├─────────────────────────┬───────────────────────────────┬────────────────────────────────────────┤
│ Dimension               │ Option A (Dual Ingest Adapter)│ Option B (Full Hard Cutover)           │
├─────────────────────────┼───────────────────────────────┼────────────────────────────────────────┤
│ Configuration Toggle    │ `STORAGE_PROVIDER=gcs|r2`     │ Hardcoded to GCS only                  │
│                         │ `TRANSCODER_TYPE=gcp|docker`  │                                        │
│ Risk of Regression      │ **Lowest**: Immediate fallback│ Moderate: Requires all R2 code deletion│
│ Testing Flexibility     │ High: Can run both in parallel│ Low: One active provider only          │
│ Codebase Cleanliness    │ Clean via IoC interfaces      │ Minimal footprint                      │
│ **Selected Strategy**   │ **Option A (IoC Multi-Adapter)**: Keeps existing tests intact.          │
└─────────────────────────┴───────────────────────────────┴────────────────────────────────────────┘
```
