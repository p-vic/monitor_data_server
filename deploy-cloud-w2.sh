#!/bin/bash
# Despliegue en nodo NUBE
# Uso: ./deploy-cloud-w2.sh [up|down|restart|logs]

set -e
COMPOSE_FILE="docker-compose.cloud.yml"
ENV_FILE=".env.cloud"

if [ ! -f "$ENV_FILE" ]; then
  echo "ERROR: No se encontró $ENV_FILE"
  exit 1
fi

ACTION=${1:-up}

case "$ACTION" in
  up)
    echo "Desplegando nodo nube..."
    docker compose -f $COMPOSE_FILE --env-file $ENV_FILE up -d --build --pull never
    echo "Listo. Servicios activos:"
    docker compose -f $COMPOSE_FILE --env-file $ENV_FILE ps
    ;;
  down)
    docker compose -f $COMPOSE_FILE --env-file $ENV_FILE down
    ;;
  restart)
    docker compose -f $COMPOSE_FILE --env-file $ENV_FILE down
    docker compose -f $COMPOSE_FILE --env-file $ENV_FILE up -d --build --pull never
    ;;
  pull)
    echo "Actualizando imágenes base desde Docker Hub..."
    docker compose -f $COMPOSE_FILE --env-file $ENV_FILE pull
    ;;
  logs)
    docker compose -f $COMPOSE_FILE --env-file $ENV_FILE logs -f
    ;;
  *)
    echo "Uso: $0 [up|down|restart|logs]"
    exit 1
    ;;
esac
