#!/usr/bin/env bash
set -euo pipefail

PROJECT_ID="${PROJECT_ID:-}"
DESCRIPTION="${DESCRIPTION:-}"
WORKSPACE="${WORKSPACE:-.}"
GOCACHE="${GOCACHE:-${WORKSPACE}/.cache/go-build}"

if [ -n "${PROJECT_ID}" ]; then
  echo "==> Using project id: ${PROJECT_ID}"
else
  echo "==> Using project id from config (if present)"
fi
echo "==> Workspace: ${WORKSPACE}"
echo "==> Go cache: ${GOCACHE}"

echo "==> Downloading Go modules"
GOCACHE="${GOCACHE}" go mod download

if [ -n "${CONFIG_FILE:-}" ]; then
  echo "==> Importing config into DB: ${CONFIG_FILE}"
  GOCACHE="${GOCACHE}" go run ./cmd/pl project config import --file "${CONFIG_FILE}" --workspace "${WORKSPACE}"
fi

echo "Done. DB will be created on first command if missing at ${WORKSPACE}/.proofline/proofline.db"
