# Bitmill API Contracts Specification (v1)

This document specifies the HTTP REST contracts for client-to-server communication under `/api/v1/videos`.

---

## 1. Global Response Envelope

All API endpoints return JSON conforming to the standardized response envelope:

### Success Response
```json
{
  "success": true,
  "data": { ... }
}
```

### Error Response
```json
{
  "success": false,
  "error": "Descriptive human-readable error message"
}
```

---

## 2. Video Ingestion Endpoints

### 2.1 Initiate Upload Session
- **Route**: `POST /api/v1/videos/upload-session`
- **Description**: Initializes an S3-compatible multipart upload session on Cloudflare R2, calculates adaptive chunk sizes (10MB for $\le 250\text{MB}$, 20MB for $> 250\text{MB}$), and generates presigned PUT URLs for all parts. Supports de-duplication via `file_fingerprint`.

**Request Body:**
```json
{
  "user_id": "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d",
  "title": "Introduction to Distributed Systems",
  "description": "Episode 1: Video Ingestion Architecture",
  "file_name": "lecture_01.mp4",
  "file_size": 1073741824,
  "mime_type": "video/mp4",
  "file_fingerprint": "1073741824-1719200000-a1b2c3d4e5f6"
}
```

**Response (201 Created / 200 OK if resumed):**
```json
{
  "success": true,
  "data": {
    "video_id": "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d",
    "upload_id": "AIRfQ5vjBRRXPICzvJfNwm...",
    "part_size_bytes": 20971520,
    "total_parts": 52,
    "expires_at": "2026-10-28T12:00:00Z",
    "parts": [
      {
        "part_number": 1,
        "upload_url": "https://<account_id>.r2.cloudflarestorage.com/bitmill-raw-videos/raw/...partNumber=1&X-Amz-Signature=..."
      }
    ],
    "is_resumed": false
  }
}
```

---

### 2.2 Query Upload Status & Resume Parts
- **Route**: `GET /api/v1/videos/:id/upload-status`
- **Description**: Used by clients upon reconnecting or reloading the page. Returns the list of acknowledged chunks and fresh presigned PUT URLs for all remaining missing chunks.

**Response (200 OK):**
```json
{
  "success": true,
  "data": {
    "video_id": "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d",
    "status": "UPLOADING",
    "total_parts": 52,
    "completed_parts_count": 51,
    "completed_parts": [
      {
        "part_number": 1,
        "etag": "\"8b8378787c0925f42ccb829f6cc2fb97\""
      }
    ],
    "missing_parts": [
      {
        "part_number": 52,
        "upload_url": "https://<account_id>.r2.cloudflarestorage.com/bitmill-raw-videos/...partNumber=52&X-Amz-Signature=..."
      }
    ]
  }
}
```

---

### 2.3 Finalize & Complete Upload
- **Route**: `POST /api/v1/videos/:id/complete-upload`
- **Description**: Submits the array of part numbers and corresponding Cloudflare R2 ETags to assemble the raw video object and enqueue the background transcoding task. Idempotent against network drops at 99%.

**Request Body:**
```json
{
  "parts": [
    {
      "part_number": 1,
      "etag": "\"8b8378787c0925f42ccb829f6cc2fb97\""
    },
    {
      "part_number": 2,
      "etag": "\"3b6a08c4fcfeae4613f8daf07459aab1\""
    }
  ]
}
```

**Response (200 OK):**
```json
{
  "success": true,
  "data": {
    "video_id": "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d",
    "status": "UPLOADED",
    "message": "Upload completed successfully. Ready for transcoding."
  }
}
```

---

### 2.4 Abort Upload Session
- **Route**: `DELETE /api/v1/videos/:id/upload-session`
- **Description**: Explicitly aborts the multipart upload session, purging all staged parts from Cloudflare R2 and releasing allocated database locks.

**Response (200 OK):**
```json
{
  "success": true,
  "data": {
    "video_id": "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d",
    "status": "ABORTED",
    "message": "Upload session aborted and storage reclaimed"
  }
}
```
