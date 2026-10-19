#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

if [ ! -f .env ]; then
  cp .env.example .env
  echo "created .env"
fi

docker compose -p cindy-middlewares -f deploy/compose/docker-compose.yml up -d
echo "waiting for middlewares..."
sleep 5

go run ./cmd/migrate
go run ./cmd/seed

exec go run ./cmd/server
