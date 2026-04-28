#!/bin/bash
# Data Plane — Worker 1 (Default) (191.101.14.88 → migrando a 38.22.34.115)
# UUID: 11111111-1111-1111-1111-111111111111
# Uso: ./deploy-w1.sh [up|down|restart|pull|logs]

set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="$SCRIPT_DIR/docker-compose-w1.yml"
ENV_FILE="$SCRIPT_DIR/.env.w1"

if [ ! -f "$ENV_FILE" ]; then
  echo "ERROR: No se encontró $ENV_FILE"
  exit 1
fi

ACTION=${1:-up}

case "$ACTION" in
  up)
    echo "Desplegando Worker 1 (Default)..."
    docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d --build --pull never
    echo "Listo. Servicios activos:"
    docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" ps
    ;;
  down)
    docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" down
    ;;
  restart)
    docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" down
    docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d --build --pull never
    ;;
  pull)
    echo "Actualizando imágenes base desde Docker Hub..."
    docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" pull
    ;;
  logs)
    docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" logs -f
    ;;
  *)
    echo "Uso: $0 [up|down|restart|pull|logs]"
    exit 1
    ;;
esac
