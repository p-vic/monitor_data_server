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

BACKUP_SIZE=$(du -sh "$OUT_TAR" | cut -f1)
echo "[$(date)] Backup completado: ${BACKUP_SIZE}"

STATUS_FILE="/home/victor/backups/backup-status.json"
python3 -c "
import json, os, sys
f, cat, key, ts, sz = sys.argv[1:]
d = json.load(open(f)) if os.path.exists(f) else {}
d.setdefault(cat, {})[key] = {'last_run': ts, 'size': sz, 'status': 'ok'}
json.dump(d, open(f, 'w'), indent=2)
" "$STATUS_FILE" "influxdb" "$WORKER" "$(date '+%Y-%m-%d %H:%M')" "$BACKUP_SIZE" || true

# Rotar — eliminar backups con más de KEEP_WEEKS semanas
find "$BACKUP_DIR" -name "${WORKER}_*.tar.gz" -mtime +$((KEEP_WEEKS * 7)) -delete
echo "[$(date)] Rotación completada (retención: ${KEEP_WEEKS} semanas)"
