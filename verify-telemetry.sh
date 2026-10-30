#!/bin/bash
# Verifica que la telemetría sigue fluyendo, vista desde el control plane.
# Pensado para correr antes y después de aplicar firewall-ports.sh y comparar.
#
# Para cada MonitorServer activo comprueba, desde dentro de ips_control_plane:
#   - InfluxDB /ping          (204 esperado)
#   - puntos `pings` últimos 2 min (debe ser > 0 si el worker tiene targets)
#   - worker ip_endpoint /health (cualquier respuesta HTTP = alcanzable)
#
# Uso: sudo ./verify-telemetry.sh
#   PGUSER / PGDB para otro usuario o base (por defecto admin / ipsmonitor)

set -uo pipefail

PGUSER=${PGUSER:-admin}
PGDB=${PGDB:-ipsmonitor}
SEP=$'\x1f'

echo "=== $(date '+%F %T') ==="

echo "--- Reglas DOCKER-USER"
iptables -n -v -L DOCKER-USER --line-numbers 2>/dev/null || echo "(sin permisos o cadena inexistente)"

echo "--- Sockets en escucha (8081, 8083, 8085-8087)"
ss -ltnp | grep -E ':(8081|8083|808[5-7])\b' || echo "(ninguno)"

echo "--- Control plane -> workers"
read -r -d '' CHECK_PY <<'EOF'
import csv, io, sys, urllib.error, urllib.parse, urllib.request

FLUX = (
    'from(bucket:"{bucket}") |> range(start:-2m)'
    ' |> filter(fn:(r) => r._measurement == "pings" and r._field == "latency_ms")'
    ' |> group() |> count()'
)


def http(url, data=None, headers=None):
    req = urllib.request.Request(url, data=data, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=5) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, ""
    except Exception as e:
        return None, type(e).__name__


def count_points(body):
    total, idx = 0, None
    for row in csv.reader(io.StringIO(body)):
        if "_value" in row:
            idx = row.index("_value")
        elif idx is not None and len(row) > idx and row[idx].isdigit():
            total += int(row[idx])
    return total


failed = False
print(f"{'worker':<28} {'influx /ping':<14} {'puntos 2m':<12} worker /health")
for line in sys.stdin:
    name, endpoint, url, token, org, bucket = line.rstrip("\n").split("\x1f")
    url = url.rstrip("/")

    ping, err = http(f"{url}/ping")
    ping_txt = str(ping) if ping else f"FALLO ({err})"

    status, body = http(
        f"{url}/api/v2/query?org={urllib.parse.quote(org)}",
        data=FLUX.format(bucket=bucket).encode(),
        headers={
            "Authorization": f"Token {token}",
            "Content-Type": "application/vnd.flux",
            "Accept": "application/csv",
        },
    )
    points_txt = str(count_points(body)) if status == 200 else f"FALLO ({status or body})"

    if endpoint:
        health, err = http(endpoint.rstrip("/") + "/health")
        health_txt = str(health) if health else f"FALLO ({err})"
    else:
        health, health_txt = 0, "(sin endpoint)"

    failed |= ping != 204 or status != 200 or health is None
    print(f"{name[:27]:<28} {ping_txt:<14} {points_txt:<12} {health_txt}")

sys.exit(1 if failed else 0)
EOF

# Los tokens viajan solo por el pipe, nunca como argumentos de proceso.
docker exec ips_postgres psql -U "$PGUSER" -d "$PGDB" -At -F "$SEP" -c "
  SELECT name, coalesce(ip_endpoint, ''), influxdb_url, influxdb_token, influxdb_org, influxdb_bucket
  FROM monitor_servers
  WHERE is_active AND influxdb_url NOT IN ('', 'http://pending:8086')
  ORDER BY name" \
| docker exec -i ips_control_plane python -c "$CHECK_PY"
rc=$?
[[ $rc -eq 0 ]] && echo "RESULTADO: OK" || echo "RESULTADO: HAY FALLOS (rc=$rc)"
exit $rc
