---
name: environment-parity-protocol
description: >-
  Enforce strict environment parity and cloud resource isolation across Local, Dev, and Production environments.
  Prevents cross-environment configuration drift, hardcoded secrets, container networking mismatches,
  CORS/storage bucket pollution, and deployment surprises between localhost and production servers.
---

# Environment Parity & Cloud Isolation Protocol

This skill codifies the architectural rules, checklist, and operational standards to guarantee 100% environment parity between local development and production deployments, preventing configuration drift, network host collisions, and cloud permission failures.

---

## 1. The 5 Pillars of Environment Parity

```
┌─────────────────────────────────────────────────────────────────────────┐
│                     THE 5 PARITY PILLARS                                │
├─────────────────────────┬───────────────────────────────────────────────┤
│ 1. Cascading Config     │ os.Environ > .env.<APP_ENV> > .env.local/prod │
│ 2. Cloud Resource Split │ Isolated -dev and -prod GCS buckets & queues  │
│ 3. Zero-Disk Secrets    │ In-memory base64/JSON creds (no credentials)  │
│ 4. Scoped CORS Security │ Dev allows localhost; Prod locked to FQDN     │
│ 5. Modular Makefiles    │ Separate Makefile.gcp & Makefile.prod         │
└─────────────────────────┴───────────────────────────────────────────────┘
```

### Pillar 1: Dual-Environment File Separation & Cascading Resolution
- **Never maintain a single monolithic `.env` file** that mixes localhost and production endpoints.
- Maintain two explicit files:
  1. `.env.local`: Configured for host development (`localhost:5432`, `localhost:5672`, dev buckets).
  2. `.env.prod`: Configured for containerized production (`postgres:5432`, `rabbitmq:5672`, prod buckets, release mode).
- **Application Loader Contract**:
  - The application configuration loader MUST evaluate environment files gracefully without terminating on missing candidates:
    ```go
    candidates := []string{
        ".env." + appEnv,
        ".env.local",
        ".env.prod",
        ".env",
    }
    for _, file := range candidates {
        if _, err := os.Stat(file); err == nil {
            _ = godotenv.Load(file)
        }
    }
    ```
  - Standard precedence: OS / Container Environment Variables > `.env.<APP_ENV>` > `.env.local` > `.env.prod` > `.env`.

### Pillar 2: Isolated Cloud Storage & Naming Standard
- **Never share storage buckets or queues between Dev and Prod.**
- **Bucket Naming Convention**:
  - Dev/Local: `<project>-raw-videos-dev` and `<project>-processed-videos-dev`
  - Prod: `<project>-raw-videos-prod` and `<project>-processed-videos-prod`
- **Bucket Invariants**:
  - Raw Ingestion Buckets: 100% Private (No public read). Governed by a 7-day auto-deletion lifecycle rule to control cloud storage costs.
  - Processed HLS Buckets: Read-only public access (`allUsers` has `roles/storage.objectViewer`) for HLS/DASH playback. Writes restricted strictly to the Cloud Transcoder Service Agent.

### Pillar 3: Zero-Disk Secret Management
- **Never commit physical JSON credential files** (`credentials.json`, `service-account.json`) to the file system.
- Pass service account credentials strictly through in-memory environment variables:
  - `GCP_CREDENTIALS_JSON='{"type": "service_account", ...}'`
- The cloud storage adapter and transcoder client must parse credentials directly from memory via `option.WithCredentialsJSON([]byte(val))` before falling back to local files or Application Default Credentials (ADC).

### Pillar 4: Scoped CORS Security Policies
- **Never use wildcard `origin: ["*"]` in Production.**
- **Dev CORS Policy** (`environment/gcp/cors-dev.json`):
  - Must explicitly declare all developer frontend ports:
    - `http://localhost:8080`, `http://127.0.0.1:8080` (Backend API / Local UI)
    - `http://localhost:3000`, `http://127.0.0.1:3000` (React / Next.js)
    - `http://localhost:5173`, `http://127.0.0.1:5173` (Vite)
    - Staging/Dev domain (e.g. `https://devlungtung.htcwood.com`)
- **Prod CORS Policy** (`environment/gcp/cors-prod-*.json`):
  - Strictly locked to the verified production Fully Qualified Domain Name (FQDN): `["https://devlungtung.htcwood.com"]`.

### Pillar 5: Modular & Icon-Free Operational Makefiles
- Keep the root `Makefile` concise by delegating infrastructure and operational tasks into modular files:
  - `Makefile.gcp`: All Cloud IAM, bucket creation, CORS sync, and Transcoder verification.
  - `Makefile.prod`: All production lifecycle targets (`prod-pull`, `prod-up`, `prod-down`, `prod-logs`, `prod-status`, `prod-smoke-test`).
- **Formatting Rule**: Avoid decorative emojis/icons in operational Makefiles. Maintain pure Unix-standard terminal output.

---

## 2. Pre-Flight Production Deployment Checklist

Before triggering production deployment or declaring a migration complete, verify each gate:

- [ ] **Gate 1: Cloud IAM & Service Agents**
  - Transcoder Service Agent (`service-<PROJECT_NUMBER>@gcp-sa-transcoder.iam.gserviceaccount.com`) has `roles/storage.objectAdmin` on the processed bucket and `roles/storage.objectViewer` on the raw bucket.
  - Verification: `make gcp-check-iam` returns valid role bindings.

- [ ] **Gate 2: CORS & Lifecycle Policies**
  - Dev buckets allow localhost ports; Prod buckets lock to official domain.
  - Raw buckets have 7-day auto-delete lifecycle policy applied.
  - Verification: `make gcp-apply-cors && make gcp-apply-lifecycle`.

- [ ] **Gate 3: Docker Compose Environment Variable Overrides**
  - Verify that `environment/docker-compose.prod.yml` does NOT hardcode override values that contradict `.env.prod` (e.g. `TRANSCODER_TYPE=native` hardcoded instead of `${TRANSCODER_TYPE:-gcp}`).
  - Ensure `--env-file .env.prod` is injected into `COMPOSE_PROD`.

- [ ] **Gate 4: Git Secret Sanitization**
  - Ensure `.env`, `.env.local`, `.env.prod`, and `.env.production` are strictly tracked in `.gitignore`.
  - Verification: `git status -s` shows zero tracked secret files.

- [ ] **Gate 5: Smoke Test & Connectivity**
  - Execute `make prod-smoke-test` to verify Gateway HTTP 200/OK.
  - Inspect live logs via `make prod-logs` to confirm clean database and message queue connections.

---

## 3. FMEA Failure Prevention Matrix

| Failure Mode | Root Cause | Impact | Automated Prevention |
| :--- | :--- | :--- | :--- |
| **Worker uses local FFmpeg on Prod** | `docker-compose.prod.yml` hardcodes `TRANSCODER_TYPE=native` under worker environment block. | Worker fails or attempts local CPU encoding inside lightweight container without static FFmpeg binary. | Use dynamic variable default `${TRANSCODER_TYPE:-gcp}` in Compose. |
| **Transcoder Job Failed (Atom 0 missing audio)** | Silent video without audio stream submitted to fixed audio transcode ladder. | GCP Transcoder returns 400 error. Transcoding halts. | Dual-end moov inspection + Self-healing auto-recovery resubmitting without audio track. |
| **Web Player CORS Error on Prod** | Processed bucket missing CORS or using outdated domain origins. | HLS video player displays black screen; browser blocks `.m3u8` / `.ts` chunks. | Apply `cors-prod-processed.json` via `make gcp-apply-cors`. |
| **Permission Denied on Bucket Read/Write** | GCP Transcoder Service Agent not provisioned or lacks IAM role on target bucket. | Transcoder jobs get rejected immediately with 403 Forbidden. | Run `gcloud beta services identity create` and execute `make gcp-check-iam`. |
| **Localhost DB Connection Refused** | Local app reads production `.env` pointing to `postgres:5432` rather than `localhost:5432`. | API crashes on startup with `dial tcp: connection refused`. | Dedicated `.env.local` loaded automatically by `config.go`. |
| **Secrets Leaked to GitHub** | `.env.local` or `.env.prod` omitted from `.gitignore`. | GCP Service Account credentials or DB passwords exposed publicly. | Enforce `.env*` wildcard in `.gitignore`. |

---

## 4. Standard Directory & Configuration Layout

```
.
├── Makefile                     # Root developer automation (includes sub-makefiles)
├── Makefile.gcp                 # Dedicated Google Cloud Platform automation
├── Makefile.prod                # Dedicated Production container operations
├── .gitignore                   # Ignores .env, .env.local, .env.prod, .env.production
├── .env.example                 # Public reference template for team members
├── .env.local                   # Ignored: Local developer configuration (localhost)
├── .env.prod                    # Ignored: Production server configuration (container networks)
├── environment/
│   ├── docker-compose.yml       # Local developer stack (exposes host ports)
│   ├── docker-compose.prod.yml  # Zero-trust production stack (isolated networks)
│   └── gcp/
│       ├── cors-dev.json        # Localhost ports + dev domains
│       ├── cors-prod-raw.json   # FQDN PUT/OPTIONS rules
│       ├── cors-prod-processed.json # FQDN GET/HEAD/OPTIONS rules
│       └── lifecycle-raw.json   # 7-day auto-purge policy for raw ingestion
└── go-streaming/
    └── internal/config/
        └── config.go            # Cascading loader: os.Env > .env.<APP_ENV> > .env.local > .env
```
