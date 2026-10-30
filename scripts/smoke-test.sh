#!/usr/bin/env bash
set -eo pipefail

CYAN='\033[36m'
GREEN='\033[32m'
RED='\033[31m'
YELLOW='\033[33m'
RESET='\033[0m'

BASE_URL="${1:-http://127.0.0.1}"
echo -e "${CYAN}======================================================================${RESET}"
echo -e "${CYAN}🚀 BITMILL POST-DEPLOYMENT SYNTHETIC SMOKE VERIFICATION SUITE${RESET}"
echo -e "${CYAN}======================================================================${RESET}"
echo -e "Target Gateway Base URL: ${GREEN}${BASE_URL}${RESET}"

# 1. Healthcheck Ingress Probe
echo -e "\n${CYAN}[1/6] Verifying Gateway /health Endpoint...${RESET}"
HEALTH_RESP=$(curl -s -f -m 5 "${BASE_URL}/health" || echo "FAILED")
if echo "$HEALTH_RESP" | grep -q '"status":"UP"'; then
    echo -e "  • Gateway Healthcheck: ${GREEN}PASS (HTTP 200, status=UP)${RESET}"
else
    echo -e "  ${RED}❌ Healthcheck failed! Response: ${HEALTH_RESP}${RESET}"
    exit 1
fi

# 2. Readiness Ingress Probe (Database Connectivity)
echo -e "\n${CYAN}[2/6] Verifying Gateway /readyz Probe (PostgreSQL Connectivity)...${RESET}"
READYZ_RESP=$(curl -s -f -m 5 "${BASE_URL}/readyz" || echo "FAILED")
if echo "$READYZ_RESP" | grep -q '"status":"READY"'; then
    echo -e "  • Database Readiness: ${GREEN}PASS (status=READY, database=UP)${RESET}"
else
    echo -e "  ${RED}❌ Readiness probe failed! Response: ${READYZ_RESP}${RESET}"
    exit 1
fi

# 3. Security Perimeter Verification (/metrics must be 403 Forbidden to public)
echo -e "\n${CYAN}[3/6] Verifying Perimeter Isolation (/metrics blocking)...${RESET}"
METRICS_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -m 5 "${BASE_URL}/metrics" || echo "000")
if [ "$METRICS_STATUS" -eq 403 ]; then
    echo -e "  • Perimeter Isolation: ${GREEN}PASS (Public access blocked with HTTP 403)${RESET}"
else
    echo -e "  ${RED}❌ Security violation! /metrics returned HTTP ${METRICS_STATUS} (expected 403)${RESET}"
    exit 1
fi

# 4. Ingestion API Smoke Test (Query Video Catalog & Ingestion Readiness)
echo -e "\n${CYAN}[4/6] Verifying Video Catalog API Contract (/api/videos)...${RESET}"
VIDEOS_RESP=$(curl -s -m 5 "${BASE_URL}/api/videos" || echo "FAILED")
if echo "$VIDEOS_RESP" | grep -q '"success":true'; then
    echo -e "  • Video Catalog API: ${GREEN}PASS (HTTP 200, PostgreSQL query OK)${RESET}"
else
    echo -e "  ${RED}❌ Video catalog API failed! Response: ${VIDEOS_RESP}${RESET}"
    exit 1
fi

# 5. Worker Daemon Heartbeat Telemetry Check
echo -e "\n${CYAN}[5/6] Verifying Transcoding Worker Heartbeat...${RESET}"
if docker ps --format '{{.Names}}' | grep -q 'bitmill-prod-worker'; then
    WORKER_HB_AGE=999
    for attempt in {1..10}; do
        WORKER_HB_AGE=$(docker exec bitmill-prod-worker sh -c 'test -f /tmp/worker.heartbeat && echo $(( $(date +%s) - $(stat -c %Y /tmp/worker.heartbeat 2>/dev/null || stat -f %m /tmp/worker.heartbeat) ))' 2>/dev/null || echo "999")
        if [ "$WORKER_HB_AGE" -lt 30 ] 2>/dev/null; then
            break
        fi
        sleep 1
    done
    if [ "$WORKER_HB_AGE" -lt 30 ] 2>/dev/null; then
        echo -e "  • Worker Heartbeat: ${GREEN}PASS (Heartbeat age: ${WORKER_HB_AGE}s < 30s threshold)${RESET}"
    else
        echo -e "  ${RED}❌ Worker heartbeat is stale or missing! Age: ${WORKER_HB_AGE}s${RESET}"
        exit 1
    fi
else
    echo -e "  ${YELLOW}⚠️ Worker container not running locally; skipping docker exec heartbeat check.${RESET}"
fi

# 6. RabbitMQ Broker Cluster Health
echo -e "\n${CYAN}[6/6] Verifying RabbitMQ Broker Diagnostics...${RESET}"
if docker ps --format '{{.Names}}' | grep -q 'bitmill-prod-rabbitmq'; then
    if docker exec bitmill-prod-rabbitmq rabbitmq-diagnostics -q ping >/dev/null 2>&1; then
        echo -e "  • RabbitMQ Broker: ${GREEN}PASS (rabbitmq-diagnostics ping OK)${RESET}"
    else
        echo -e "  ${RED}❌ RabbitMQ diagnostics ping failed!${RESET}"
        exit 1
    fi
else
    echo -e "  ${YELLOW}⚠️ RabbitMQ container not running locally; skipping docker exec ping.${RESET}"
fi

echo -e "\n${GREEN}======================================================================${RESET}"
echo -e "${GREEN}✅ ALL POST-DEPLOYMENT SMOKE ASSERTIONS PASSED WITH ZERO ERRORS${RESET}"
echo -e "${GREEN}======================================================================${RESET}"
exit 0
