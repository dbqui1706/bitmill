---
name: environment-setup
description: >-
  Universal environment setup, configuration parity, and resource isolation framework for any software project
  across Local, Dev, Staging, and Production. Automates dual-env separation, zero-disk secrets, cloud resource
  partitioning (AWS/GCP/Cloudflare/Azure), container network isolation, scoped CORS, and pre-flight verification gates.
---

# Universal Environment Setup & Parity Protocol

This skill codifies the standard engineering procedures to set up, configure, and isolate development and production environments across any technology stack (Go, Node.js/TypeScript, Python, Java, Docker, Kubernetes). It eliminates configuration drift, container networking collisions, credential leaks, and deployment surprises.

---

## 1. Core Invariants & Engineering Guardrails

1. **The 12-Factor Parity Invariant**: Keep development, staging, and production environments as identical as possible. Never branch business logic or code execution paths based on environment flags—only external configuration values change.
2. **Strict Cloud Resource Partitioning**: Local development and production environments must NEVER share stateful cloud resources (databases, object storage buckets, message queues, caches).
3. **Zero-Disk Secret Management**: Secrets, API keys, and Service Account certificates must never reside as plaintext files on server disks or in version control. They must be resolved via in-memory environment variables or cloud secret managers.
4. **Deterministic Cascading Precedence**: Configuration loading must follow a strict, non-destructive hierarchy:
   `OS / Container Runtime Env` > `Cloud Secret Manager / Vault` > `.env.<APP_ENV>` > `.env.local` > `.env.prod` > `.env`.
5. **Scoped CORS Boundaries**: Local development whitelists explicit developer localhost ports; production strictly whitelists verified Fully Qualified Domain Names (FQDNs). Never use `origin: ["*"]` in Production.

---

## 2. The 5 Universal Pillars of Environment Parity

```
┌─────────────────────────────────────────────────────────────────────────┐
│                     THE 5 PARITY PILLARS                                │
├─────────────────────────┬───────────────────────────────────────────────┤
│ 1. Cascading Config     │ Runtime Env > .env.<APP_ENV> > .env.local/prod│
│ 2. Cloud Resource Split │ Isolated -dev, -staging, -prod cloud assets   │
│ 3. Zero-Disk Secrets    │ In-memory JSON/Base64 credentials             │
│ 4. Network & Container  │ Host ports (Local) vs Bridge service (Prod)   │
│ 5. Scoped CORS Security │ Localhost ports (Dev) vs Locked FQDN (Prod)   │
└─────────────────────────┴───────────────────────────────────────────────┘
```

### Pillar 1: Dual-Environment File Separation & Cascading Resolution
- **Rule**: Never maintain a single monolithic `.env` file that mixes local development values with production infrastructure endpoints.
- **File Structure**:
  - `.env.local`: Configured for host development (`localhost:5432`, `localhost:6379`, dev buckets/queues).
  - `.env.prod`: Configured for containerized production (`postgres:5432`, `redis:6379`, prod buckets/queues).
  - `.env.example`: Sanitized reference template committed to Git (contains zero secrets).

- **Implementation Patterns Across Languages**:
  - **Go**: Gracefully verify file existence before loading to avoid aborting on missing candidates:
    ```go
    appEnv := os.Getenv("APP_ENV")
    if appEnv == "" { appEnv = "local" }
    candidates := []string{".env." + appEnv, ".env.local", ".env.prod", ".env"}
    for _, file := range candidates {
        if _, err := os.Stat(file); err == nil {
            _ = godotenv.Load(file)
        }
    }
    ```
  - **Node.js / TypeScript**: Load environment-specific files sequentially:
    ```typescript
    import dotenv from 'dotenv';
    const env = process.env.NODE_ENV || 'development';
    ['.env.' + env, '.env.local', '.env'].forEach((file) => {
      dotenv.config({ path: file, override: false });
    });
    ```
  - **Python**: Use `pydantic-settings` with multiple env sources:
    ```python
    from pydantic_settings import BaseSettings, SettingsConfigDict
    class Settings(BaseSettings):
        model_config = SettingsConfigDict(
            env_file=('.env.' + os.getenv('APP_ENV', 'local'), '.env.local', '.env'),
            env_file_encoding='utf-8',
            extra='ignore'
        )
    ```

### Pillar 2: Cloud Resource Partitioning & Storage Lifecycles
- **Naming Convention**: `<project>-<resource>-<env>` (e.g., `acme-uploads-dev`, `acme-uploads-prod`).
- **Storage Tier Isolation (AWS S3, GCP GCS, Cloudflare R2, Azure Blob)**:
  - **Raw / Ingestion Assets**: 100% Private (No public read). Governed by an automated retention/deletion lifecycle policy (e.g., 7 or 14 days) to prevent runaway cloud storage bills.
  - **Processed / Public Assets**: Read-only public access or routed via a CDN (Cloudflare, CloudFront, Cloud CDN). Write permissions restricted strictly to specialized backend worker/agent identities.

### Pillar 3: Zero-Disk Secret Management
- **Rule**: Never mount or persist physical credential files (e.g., `credentials.json`, `service-account.json`, `.pem`) on application server disks.
- **In-Memory Credential Loading**:
  - Pass structured secrets via environment variables (e.g., `GCP_CREDENTIALS_JSON='{"type":"service_account",...}'`, `AWS_SECRET_ACCESS_KEY`).
  - Initialize cloud SDK clients directly from in-memory byte streams or use cloud native metadata providers (AWS IAM Roles for EC2/EKS, GCP Workload Identity / Attached Service Accounts).
- **Git Shield**:
  - Root `.gitignore` must always enforce:
    ```gitignore
    .env
    .env.local
    .env.prod
    .env.production
    .env.*.local
    *.pem
    *.key
    ```

### Pillar 4: Container Network Isolation & Dual-Boundary
- **The Local vs Container Network Problem**:
  - Local developers access dependencies via host-forwarded ports (`localhost:5432`, `localhost:6379`, `localhost:5672`).
  - Production containers communicate across internal Docker bridge or Kubernetes overlay networks (`postgres:5432`, `redis:6379`, `rabbitmq:5672`).
- **Compose Manifest Guardrail**:
  - Never hardcode environment variable values in production compose or manifest files that silently override `.env.prod`.
  - Always use dynamic fallback expansion: `${VARIABLE_NAME:-default_value}`.
  - Inject environment files explicitly at runtime: `docker compose --env-file .env.prod -f docker-compose.prod.yml up -d`.

### Pillar 5: Scoped CORS Security Boundaries
- **Rule**: Never set `origin: ["*"]` in Production storage buckets, API gateways, or backend services.
- **Dev CORS Policy**:
  - Explicitly whitelist standard local development ports:
    - Backend/API default ports (`http://localhost:8080`, `http://127.0.0.1:8080`)
    - Frontend development servers (`http://localhost:3000`, `http://localhost:5173`, `http://localhost:4200`)
    - Staging/Dev domain (e.g., `https://dev.example.com`)
- **Prod CORS Policy**:
  - Strictly locked to verified production FQDNs (`["https://app.example.com"]`).

---

## 3. Pre-Flight Production Deployment Checklist

Execute this checklist before running production deployment on any software project:

```
[Pre-Flight Verification]
        │
        ├─► Gate 1: Cloud IAM & Service Identities verified on production assets
        ├─► Gate 2: Dev & Prod cloud resources partitioned (zero shared state)
        ├─► Gate 3: CORS policies applied (Dev: localhost / Prod: FQDN)
        ├─► Gate 4: Zero hardcoded overrides in Compose / Helm / K8s manifests
        ├─► Gate 5: .env* files verified in .gitignore (Zero Secrets in Git)
        └─► Gate 6: Smoke test probe passed (Gateway HTTP 200/OK on /health)
```

1. **Gate 1: Cloud IAM & Access Permissions**: Verify that runtime service identities (Task runners, Background workers, Lambdas) have explicit IAM permissions on production databases, queues, and storage buckets.
2. **Gate 2: Resource Partitioning**: Confirm production configuration does not reference any `-dev`, `-staging`, or test cloud assets.
3. **Gate 3: CORS Validation**: Confirm production storage buckets and API endpoints do not expose wildcard `*` origins and strictly list the verified production domain.
4. **Gate 4: Compose / Manifest Audit**: Inspect `docker-compose.prod.yml` or Kubernetes manifests for hardcoded variables that would silently override `.env.prod`.
5. **Gate 5: Git Secret Sanitization**: Verify `git status` shows no `.env.local`, `.env.prod`, or credential files staged or tracked in version control.
6. **Gate 6: Smoke Test Probe**: Execute an automated healthcheck against the production gateway (`/health`) and inspect logs for clean dependency connection initialization.

---

## 4. Universal FMEA Failure Prevention Matrix

| Failure Mode | Root Cause | Impact | Automated Prevention Rule |
| :--- | :--- | :--- | :--- |
| **Container ignores runtime env** | Compose file or K8s manifest hardcodes value under `environment:` block. | Container ignores `.env.prod` setting and runs outdated or dev engine. | Always use `${VAR:-fallback}` in Compose and manifest files. |
| **Localhost vs Container Network Collision** | Monolithic `.env` mixes host ports (`localhost:5432`) with container DNS names (`postgres:5432`). | App crashes on boot with `connection refused` or `ENOTFOUND`. | Enforce separate `.env.local` (host ports) and `.env.prod` (container DNS). |
| **Cloud Worker Permission Denied** | Background worker identity lacks IAM role on production cloud storage or queue. | Async jobs fail immediately with 403 Forbidden. | Verify IAM policies as a blocking gate prior to production traffic cutover. |
| **Frontend Cross-Origin Block (CORS)** | Storage bucket or API gateway missing client origin or using wildcard with credentials. | Browser blocks API calls or asset downloads; blank screen for users. | Separate dev CORS (whitelisting localhost ports) and prod CORS (whitelisting FQDN). |
| **Secrets Leaked to Remote VCS** | Incomplete `.gitignore` patterns omitting `.env.local`, `.env.prod`, or private keys. | Production credentials, database passwords, and API keys exposed publicly. | Root `.gitignore` enforces `.env*` wildcard patterns. Provide sanitized `.env.example`. |
| **Missing Required Config Crash** | New environment variable added to code but missing from production deployment config. | Application panics or crashes on boot in production. | Enforce strict schema validation on startup (Zod, Pydantic, struct validator tags). |
| **Cross-Environment Data Contamination** | Developer local environment accidentally points to production database or bucket. | Local testing corrupts, mutates, or deletes real production customer data. | Strict resource suffix convention (`-dev`, `-staging`, `-prod`) and isolated credentials. |

---

## 5. Universal Project File Layout Reference

```
.
├── Makefile                     # Root developer automation (modular includes)
├── Makefile.cloud               # Dedicated Cloud Infrastructure operations (GCP/AWS/Azure)
├── Makefile.prod                # Dedicated Production container operations
├── .gitignore                   # Ignores .env, .env.local, .env.prod, *.pem, *.key
├── .env.example                 # Public reference template for team members
├── .env.local                   # Ignored: Local developer configuration (localhost)
├── .env.prod                    # Ignored: Production server configuration (container networks)
├── deploy/ (or environment/)
│   ├── docker-compose.yml       # Local developer stack (exposes host ports)
│   ├── docker-compose.prod.yml  # Production stack (isolated internal networks)
│   └── cloud/
│       ├── cors-dev.json        # Localhost ports + dev domains
│       ├── cors-prod.json       # FQDN rules
│       └── lifecycle-policy.json # Auto-purge retention policy for temporary assets
└── config/ (or internal/config/)
    └── config.*                 # Cascading loader: os.Env > .env.<APP_ENV> > .env.local > .env
```
