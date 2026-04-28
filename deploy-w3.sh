#!/bin/bash
# Data Plane — Worker 3 (38.22.34.115)
# Secretos gestionados con Doppler (config: w3)
# Uso: ./deploy-w3.sh [up|down|restart|pull|logs]

set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="$SCRIPT_DIR/docker-compose-w3.yml"

ACTION=${1:-up}

case "$ACTION" in
  up)
    echo "Desplegando Worker 3..."
    doppler run -c w3 -- docker compose -f "$COMPOSE_FILE" up -d --build --pull never
    echo "Listo. Servicios activos:"
    doppler run -c w3 -- docker compose -f "$COMPOSE_FILE" ps
    ;;
  down)
    doppler run -c w3 -- docker compose -f "$COMPOSE_FILE" down
    ;;
  restart)
    doppler run -c w3 -- docker compose -f "$COMPOSE_FILE" down
    doppler run -c w3 -- docker compose -f "$COMPOSE_FILE" up -d --build --pull never
    ;;
  pull)
    echo "Actualizando imágenes base desde Docker Hub..."
    doppler run -c w3 -- docker compose -f "$COMPOSE_FILE" pull
    ;;
  logs)
    doppler run -c w3 -- docker compose -f "$COMPOSE_FILE" logs -f
    ;;
  *)
    echo "Uso: $0 [up|down|restart|pull|logs]"
    exit 1
    ;;
esac
