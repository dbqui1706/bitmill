# Local Environment Setup & Developer Guide

This guide covers local development, Docker containers, database migrations, and operational workflows for Bitmill.

---

## 1. Prerequisites

- **Go**: 1.23+ installed on host.
- **Docker & Docker Compose**: Docker Desktop running.
- **Make**: Standard GNU make utility.
- **Cloudflare R2**: Valid account ID and S3-compatible API credentials.

---

## 2. Environment Configuration

Copy the example environment file and populate your credentials:

```bash
cp .env.example .env
```

Key environment variables:

| Variable | Description | Example |
| :--- | :--- | :--- |
| `SERVER_HTTP_PORT` | HTTP API listening port | `8080` |
| `SERVER_RTMP_PORT` | RTMP stream ingest port | `1935` |
| `POSTGRES_HOST` | PostgreSQL container host | `localhost` |
| `POSTGRES_PORT` | PostgreSQL container port | `5432` |
| `POSTGRES_USER` | PostgreSQL user | `bitmill_user` |
| `POSTGRES_PASSWORD` | PostgreSQL password | `bitmill_password` |
| `POSTGRES_DBNAME` | PostgreSQL database name | `bitmill_media` |
| `RABBITMQ_URI` | RabbitMQ AMQP connection string | `amqp://bitmill_mq:bitmill_mq_password@localhost:5672/` |
| `CLOUDFLARE_R2_ACCOUNT_ID` | Cloudflare account identifier | `dffbddb6c2d321ad0d59aef0710a57ee` |
| `CLOUDFLARE_R2_ACCESS_KEY_ID` | S3-compatible Access Key ID | `****************` |
| `CLOUDFLARE_R2_SECRET_ACCESS_KEY`| S3-compatible Secret Access Key | `****************` |
| `CLOUDFLARE_R2_RAW_BUCKET` | Ingestion bucket name | `bitmill-raw-videos` |
| `CLOUDFLARE_R2_PROCESSED_BUCKET`| HLS distribution bucket name | `bitmill-processed-videos` |

---

## 3. Quick-Start with Makefile

All development tasks are automated via `make`:

```bash
# 1. Start all infrastructure containers (PostgreSQL & RabbitMQ)
make docker-up

# 2. Apply database migrations
make db-migrate

# 3. Run unit tests
make test

# 4. Verify live integration with real Cloudflare R2
make test-e2e

# 5. Start the API server locally
make run
```
