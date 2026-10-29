# Observability & Monitoring Guide (PLG Stack)

This document provides a comprehensive operational guide for the **Bitmill Media Platform Observability Stack**, consisting of **Prometheus**, **Grafana Loki**, **Promtail**, and **Grafana** (PLG Stack).

---

## 1. Architectural Overview

The observability stack delivers centralized, single-pane-of-glass monitoring for all platform services, replacing fragmented multi-terminal logs with unified real-time dashboards and searchable log aggregation.

```
┌────────────────────────────────────────────────────────────────────────────────────────┐
│                                  OPERATOR WORKSTATION                                  │
│                 👉 Single Entrypoint: http://localhost:3000 (Grafana)                  │
└───────────────────────────────────────────┬────────────────────────────────────────────┘
                                            │ Query Metrics & Unified Logs
                                            ▼
┌────────────────────────────────────────────────────────────────────────────────────────┐
│                               GRAFANA (Port 3000)                                      │
│       ┌───────────────────────────────────────┬────────────────────────────────┐       │
│       │ 📈 TIME-SERIES METRICS (Prometheus)   │ 📜 LIVE LOG STREAM (Loki)      │       │
│       │ • RabbitMQ Queue Depth & Delivery     │ • Real-time aggregated logs:   │       │
│       │ • Active Worker Transcoding Jobs      │   - Bitmill API & Worker       │       │
│       │ • Transcode Duration by Resolution    │   - PostgreSQL & RabbitMQ      │       │
│       │ • Go Runtime (Goroutines, Heap, GC)   │ • Level Filtering & Inspection │       │
│       └───────────────────▲───────────────────┴────────────────▲───────────────┘       │
└───────────────────────────┼────────────────────────────────────┼───────────────────────┘
                            │ Scrapes Metrics                    │ Ingests Chunks
             ┌──────────────┴──────────────┐      ┌──────────────┴──────────────┐
             │    PROMETHEUS (Port 9090)   │      │      LOKI (Port 3100)       │
             └──────────────▲──────────────┘      └──────────────▲──────────────┘
                            │ Pull Targets                       │ Push Stream
       ┌────────────────────┴────────────────────────────────────┴────────────────────┐
       │                          PROMTAIL LOG COLLECTOR                              │
       └──────────────────────────────────────────▲───────────────────────────────────┘
                                                  │ Tails Docker Containers
                 ┌────────────────────────────────┴───────────────────────────────┐
                 │                       MONITORED TARGETS                        │
                 │   • bitmill-postgres               • bitmill-rabbitmq          │
                 │   • bitmill-api (:8080/metrics)    • bitmill-worker            │
                 └────────────────────────────────────────────────────────────────┘
```

---

## 2. Port Topology & Service Endpoints

All monitoring services run as lightweight Docker containers orchestrated by `docker-compose.yml`:

| Service | Internal Port | Host Port | Purpose | Credentials / URL |
| :--- | :--- | :--- | :--- | :--- |
| **Grafana** | `3000` | `3000` | Central Unified Visualization Web UI | [http://localhost:3000](http://localhost:3000)<br>Default: `admin` / `admin` |
| **Prometheus** | `9090` | `9090` | Time-Series Metrics Database & Scraper | [http://localhost:9090](http://localhost:9090) |
| **Grafana Loki** | `3100` | `3100` | High-Efficiency Log Aggregation Engine | [http://localhost:3100](http://localhost:3100) |
| **Promtail** | `9080` | `None` | Log Collector shipping container logs | Internal Docker network |
| **RabbitMQ Mgmt** | `15672` | `15672` | RabbitMQ Web Management Dashboard | [http://localhost:15672](http://localhost:15672)<br>`bitmill_mq` / `bitmill_mq_password` |
| **RabbitMQ Metrics**| `15692` | `15692` | RabbitMQ Native Prometheus Endpoint | [http://localhost:15692/metrics](http://localhost:15692/metrics) |
| **Bitmill API Metrics**| `8080` | `8080` | Go Application Telemetry & Runtime | [http://localhost:8080/metrics](http://localhost:8080/metrics) |

---

## 3. Quick Start

### 3.1 Start the Complete Infrastructure & Monitoring
To start all databases, brokers, and the monitoring stack in the background:

```bash
make docker-up
# Or: docker compose up -d
```

Verify that all containers are healthy:
```bash
docker compose ps
```

### 3.2 Access Grafana
1. Open your browser at **[http://localhost:3000](http://localhost:3000)**.
2. Sign in with:
   - **Username**: `admin`
   - **Password**: `admin`
3. Navigate to **Dashboards > Bitmill > Bitmill Streaming Platform - Operations & Telemetry**.

---

## 4. Pre-Provisioned Dashboards & Telemetry

The platform includes **zero-touch automated provisioning** via `deployments/docker/grafana/provisioning`:

### 4.1 Data Sources
- **Prometheus** (`http://prometheus:9090`): Default time-series data source.
- **Loki** (`http://loki:3100`): Log aggregation data source.

### 4.2 Dashboard Panels
1. **RabbitMQ Queue Depth (`video.transcoding.jobs`)**:
   - Visualizes currently pending jobs waiting for worker consumption.
   - Highlights green when $\le 5$, yellow when $> 5$, red when $> 15$.
2. **Completed Transcode Jobs**:
   - Total count of successfully completed video transcodings.
3. **Go Active Goroutines & Memory Allocation**:
   - Real-time memory allocation (`go_memstats_alloc_bytes`) and concurrency health.
4. **RabbitMQ Ingestion vs. Consumption Rate**:
   - Line chart comparing incoming upload events vs. worker delivery rates over time.
5. **Transcoding Average Duration**:
   - Breakdown of transcoding duration grouped by source resolution (4K, 1440p, 1080p, 720p).
6. **Live Unified Log Stream**:
   - Real-time tailing of all Docker container outputs (`bitmill-postgres`, `bitmill-rabbitmq`, etc.) with color-coded severity levels.

---

## 5. Developer Query Cheat Sheet

### 5.1 PromQL (Prometheus Query Language)
Used in Grafana and Prometheus Web UI (`localhost:9090`):

| Metric Objective | PromQL Expression |
| :--- | :--- |
| **Pending Jobs in Queue** | `rabbitmq_queue_messages_ready{queue="video.transcoding.jobs"}` |
| **Total Jobs Finished** | `sum(bitmill_transcoder_jobs_total)` |
| **HTTP Request Rate (RPS)** | `sum(rate(bitmill_http_requests_total[1m]))` |
| **HTTP 95th Percentile Latency** | `histogram_quantile(0.95, sum(rate(bitmill_http_request_duration_seconds_bucket[5m])) by (le))` |
| **Transcoding Durations** | `rate(bitmill_transcoder_duration_seconds_sum[5m]) / rate(bitmill_transcoder_duration_seconds_count[5m])` |

### 5.2 LogQL (Grafana Loki Query Language)
Used in Grafana **Explore** tab (`localhost:3000/explore`):

| Log Search Objective | LogQL Expression |
| :--- | :--- |
| **All Bitmill Logs** | `{container=~"bitmill.*"}` |
| **Only Error Logs** | `{container=~"bitmill.*"} |= "ERROR"` |
| **RabbitMQ Container Logs** | `{container="bitmill-rabbitmq"}` |
| **PostgreSQL Error Logs** | `{container="bitmill-postgres"} |= "ERROR"` |
| **Transcoding Pipeline Logs** | `{container=~"bitmill.*"} \|~ "(?i)transcode|ffmpeg|hls"` |

---

## 6. Teardown & Maintenance

To stop the monitoring stack while preserving Prometheus and Grafana storage:
```bash
make docker-down
```

To purge all local time-series data and volumes:
```bash
docker compose down -v
```
