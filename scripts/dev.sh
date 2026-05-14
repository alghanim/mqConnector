#!/usr/bin/env bash
# Run mqconnector in development mode. Generates a self-signed certificate on
# first run, ensures a config.yaml exists, and starts the backend with verbose
# logging. The SvelteKit frontend can be run separately via `cd web && npm run
# dev` for hot-reload.
set -euo pipefail

cd "$(dirname "$0")/.."

mkdir -p data certs

# 1) Generate a self-signed cert if missing.
if [[ ! -f certs/server.crt || ! -f certs/server.key ]]; then
  echo "▸ Generating dev TLS cert (self-signed)"
  openssl req -x509 -newkey rsa:2048 -days 365 -nodes \
    -keyout certs/server.key -out certs/server.crt \
    -subj "/CN=localhost" -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" \
    >/dev/null 2>&1
fi

# 2) Make sure there is a config.yaml.
if [[ ! -f config.yaml ]]; then
  echo "▸ Seeding config.yaml from config.example.yaml"
  cp config.example.yaml config.yaml
fi

# 3) Dev defaults.
export SERVER_MODE="dev"
export LOGGING_LEVEL="debug"
export LOGGING_FORMAT="text"
export SERVER_TLS_CERT_FILE="$PWD/certs/server.crt"
export SERVER_TLS_KEY_FILE="$PWD/certs/server.key"

echo "▸ Running mqconnector (Ctrl-C to stop)"
exec go run ./cmd/mqconnector -config config.yaml
