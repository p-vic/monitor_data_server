#!/bin/bash
# InfluxDB backup — Data Plane workers
# Uso: ./backup-influx.sh [w1|w2|w3]
#
# Cron sugerido (semanal, domingos a las 03:00):
#   0 3 * * 0 /home/victor/ipmonitor/data_server/backup-influx.sh w1 >> /var/log/ips-backup.log 2>&1

set -e

WORKER=${1:-}
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKUP_DIR="/home/victor/backups/influxdb"
KEEP_WEEKS=4
DATE=$(date +%Y-%m-%d_%H-%M)

case "$WORKER" in
  w1)
    CONTAINER="ips_influxdb_w1"
    ENV_FILE="$SCRIPT_DIR/.env-w1"
    ;;
  w2)
    CONTAINER="ips_influxdb"
    ENV_FILE="$SCRIPT_DIR/.env-w2"
    ;;
  w3)
    CONTAINER="ips_influxdb_w3"
    ENV_FILE="$SCRIPT_DIR/.env-w3"
    ;;
  *)
    echo "Uso: $0 [w1|w2|w3]"
    exit 1
    ;;
esac

if [ ! -f "$ENV_FILE" ]; then
  echo "ERROR: No se encontró $ENV_FILE"
  exit 1
fi

# Leer token desde el env file del worker
INFLUX_TOKEN=$(grep "^INFLUXDB_INIT_ADMIN_TOKEN" "$ENV_FILE" | cut -d'=' -f2- | tr -d '[:space:]')
if [ -z "$INFLUX_TOKEN" ]; then
  echo "ERROR: INFLUXDB_INIT_ADMIN_TOKEN no encontrado en $ENV_FILE"
  exit 1
fi

mkdir -p "$BACKUP_DIR"
TMP_DIR="/tmp/influx_backup_${WORKER}_${DATE}"
OUT_TAR="$BACKUP_DIR/${WORKER}_${DATE}.tar.gz"

echo "[$(date)] Iniciando backup InfluxDB ($WORKER) → $OUT_TAR"

# Backup dentro del contenedor
docker exec "$CONTAINER" influx backup "$TMP_DIR" --token "$INFLUX_TOKEN"

# Copiar al host y comprimir
docker cp "${CONTAINER}:${TMP_DIR}" "/tmp/influx_backup_${WORKER}_${DATE}_host"
tar -czf "$OUT_TAR" -C "/tmp" "influx_backup_${WORKER}_${DATE}_host"

# Limpiar temporales
docker exec "$CONTAINER" rm -rf "$TMP_DIR"
rm -rf "/tmp/influx_backup_${WORKER}_${DATE}_host"

echo "[$(date)] Backup completado: $(du -sh "$OUT_TAR" | cut -f1)"

# Rotar — eliminar backups con más de KEEP_WEEKS semanas
find "$BACKUP_DIR" -name "${WORKER}_*.tar.gz" -mtime +$((KEEP_WEEKS * 7)) -delete
echo "[$(date)] Rotación completada (retención: ${KEEP_WEEKS} semanas)"
