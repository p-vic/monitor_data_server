#!/bin/bash
# =============================================================================
# IPMonitor Worker — Automated Installation Script
# =============================================================================
# Supports: Ubuntu 20.04+, Debian 11+, Raspberry Pi OS (64-bit),
#           CentOS 8+, Rocky Linux 8+, AlmaLinux 8+
#
# Usage (recommended):
#   curl -fsSL https://ipmonitor.yaurima.com/install-worker.sh -o install-worker.sh
#   bash install-worker.sh
#
# Non-interactive (CI/automated):
#   WORKER_ID=<uuid> HMAC_SECRET=<secret> bash install-worker.sh
# =============================================================================
set -euo pipefail

# ─── Colors ───────────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; CYAN='\033[0;36m'; BOLD='\033[1m'; NC='\033[0m'

info()   { echo -e "${CYAN}  →${NC}  $*"; }
ok()     { echo -e "${GREEN}  ✓${NC}  $*"; }
warn()   { echo -e "${YELLOW}  !${NC}  $*"; }
die()    { echo -e "\n${RED}  ✗  ERROR: $*${NC}" >&2; exit 1; }
header() { echo -e "\n${BOLD}${BLUE}━━━  $*  ━━━${NC}"; }
ask()    { echo -e "${BOLD}${YELLOW}  ?${NC}  $*"; }

# ─── Constants ────────────────────────────────────────────────────────────────
INSTALL_DIR="/opt/ipmonitor-worker"
CONTROL_PLANE_URL_DEFAULT="https://ipmonitor.yaurima.com"
INFLUX_ORG="monitoring"
INFLUX_BUCKET="monitorings"
GITHUB_REPO="https://github.com/p-vic/monitor_data_server.git"
WORKER_SUBDIR="go-monitoring-worker"
MIN_RAM_MB=700
MIN_DISK_GB=4

# ─── Banner ───────────────────────────────────────────────────────────────────
echo ""
echo -e "${BOLD}${BLUE}"
echo "  ╔══════════════════════════════════════════════════╗"
echo "  ║         IPMonitor Worker — Installer             ║"
echo "  ║         ipmonitor.yaurima.com                    ║"
echo "  ╚══════════════════════════════════════════════════╝"
echo -e "${NC}"

# ─── 1. Root check ────────────────────────────────────────────────────────────
header "Verificando permisos"
if [[ $EUID -ne 0 ]]; then
    die "Este script debe ejecutarse como root.\n     Ejecute: sudo bash $0"
fi
ok "Ejecutando como root"

# ─── 2. OS & Architecture Detection ──────────────────────────────────────────
header "Detectando sistema operativo"

ARCH=$(uname -m)
case "$ARCH" in
    x86_64)  ARCH_LABEL="amd64" ;;
    aarch64) ARCH_LABEL="arm64" ;;
    armv7l)  die "Arquitectura armv7 (32-bit) no soportada. Use Raspberry Pi OS de 64 bits." ;;
    *)       die "Arquitectura no soportada: $ARCH" ;;
esac

if [[ -f /etc/os-release ]]; then
    . /etc/os-release
    OS_ID="${ID:-unknown}"
    OS_NAME="${PRETTY_NAME:-$OS_ID}"
else
    die "No se pudo detectar el sistema operativo (/etc/os-release no encontrado)."
fi

# Detect Raspberry Pi
IS_RASPI=false
if [[ -f /proc/device-tree/model ]]; then
    MODEL=$(tr -d '\0' < /proc/device-tree/model 2>/dev/null || echo "")
    if echo "$MODEL" | grep -qi "raspberry"; then
        IS_RASPI=true
    fi
fi

ok "Sistema: ${OS_NAME}"
ok "Arquitectura: ${ARCH_LABEL}"
if [[ "$IS_RASPI" == "true" ]]; then
    ok "Dispositivo: Raspberry Pi detectado"
    warn "El build del worker tomará 5-15 minutos en Raspberry Pi. Tenga paciencia."
fi

# Determine package manager family
case "$OS_ID" in
    ubuntu|debian|raspbian|linuxmint)
        PKG_FAMILY="debian"
        ;;
    centos|rhel|rocky|almalinux|fedora)
        PKG_FAMILY="rhel"
        ;;
    *)
        # Try to detect from ID_LIKE
        ID_LIKE="${ID_LIKE:-}"
        if echo "$ID_LIKE" | grep -qE "debian|ubuntu"; then
            PKG_FAMILY="debian"
        elif echo "$ID_LIKE" | grep -qE "rhel|centos|fedora"; then
            PKG_FAMILY="rhel"
        else
            die "Distribución no soportada: $OS_NAME\n     Soportadas: Ubuntu, Debian, Raspberry Pi OS, CentOS, Rocky Linux, AlmaLinux"
        fi
        ;;
esac

# ─── 3. System Requirements ───────────────────────────────────────────────────
header "Verificando requisitos del sistema"

# RAM check
TOTAL_RAM_MB=$(awk '/MemTotal/ { printf "%.0f", $2/1024 }' /proc/meminfo)
if [[ "$TOTAL_RAM_MB" -lt "$MIN_RAM_MB" ]]; then
    die "RAM insuficiente: ${TOTAL_RAM_MB}MB disponibles, se requieren ${MIN_RAM_MB}MB mínimo."
fi
ok "RAM: ${TOTAL_RAM_MB}MB disponibles"

# Disk check
AVAIL_DISK_GB=$(df -BG "$HOME" | awk 'NR==2 { gsub("G",""); print $4 }')
if [[ "$AVAIL_DISK_GB" -lt "$MIN_DISK_GB" ]]; then
    die "Espacio en disco insuficiente: ${AVAIL_DISK_GB}GB disponibles, se requieren ${MIN_DISK_GB}GB mínimo."
fi
ok "Disco: ${AVAIL_DISK_GB}GB disponibles"

# Warn about SD card on Raspberry Pi
if [[ "$IS_RASPI" == "true" ]]; then
    ROOT_DEV=$(df / | awk 'NR==2{print $1}')
    if echo "$ROOT_DEV" | grep -qE "mmcblk|mmc"; then
        warn "Se detectó tarjeta SD como almacenamiento principal."
        warn "InfluxDB escribe datos continuamente. Se recomienda un SSD externo USB"
        warn "para evitar desgaste prematuro de la tarjeta SD."
    fi
fi

# curl check
if ! command -v curl &>/dev/null; then
    info "Instalando curl..."
    if [[ "$PKG_FAMILY" == "debian" ]]; then
        apt-get update -qq && apt-get install -y -qq curl
    else
        yum install -y curl 2>/dev/null || dnf install -y curl
    fi
fi
ok "curl disponible"

# ─── 4. Docker Installation ───────────────────────────────────────────────────
header "Verificando Docker"

install_docker() {
    info "Descargando script de instalación oficial de Docker..."
    curl -fsSL https://get.docker.com -o /tmp/get-docker.sh
    info "Instalando Docker (esto puede tardar unos minutos)..."
    sh /tmp/get-docker.sh --quiet
    rm -f /tmp/get-docker.sh

    systemctl enable docker --quiet
    systemctl start docker

    ok "Docker instalado y activo"
}

if command -v docker &>/dev/null; then
    DOCKER_VERSION=$(docker --version | grep -oP '\d+\.\d+\.\d+' | head -1)
    ok "Docker ya instalado: v${DOCKER_VERSION}"
else
    warn "Docker no encontrado. Instalando automáticamente..."
    install_docker
fi

# Verify Docker daemon is running
if ! docker info &>/dev/null; then
    info "Iniciando Docker daemon..."
    systemctl start docker
    sleep 3
    docker info &>/dev/null || die "No se pudo iniciar Docker. Revise: systemctl status docker"
fi

# Check Docker Compose v2
if docker compose version &>/dev/null 2>&1; then
    COMPOSE_VERSION=$(docker compose version --short 2>/dev/null || echo "v2")
    ok "Docker Compose v2 disponible (${COMPOSE_VERSION})"
else
    # Try installing compose plugin
    info "Instalando Docker Compose plugin..."
    if [[ "$PKG_FAMILY" == "debian" ]]; then
        apt-get install -y -qq docker-compose-plugin 2>/dev/null || true
    else
        yum install -y docker-compose-plugin 2>/dev/null || \
        dnf install -y docker-compose-plugin 2>/dev/null || true
    fi
    docker compose version &>/dev/null 2>&1 || \
        die "Docker Compose v2 no disponible. Instale Docker Engine >= 20.10."
    ok "Docker Compose plugin instalado"
fi

# ─── 5. Existing Installation Check ──────────────────────────────────────────
header "Verificando instalación previa"

if [[ -d "$INSTALL_DIR" ]]; then
    warn "Ya existe una instalación en ${INSTALL_DIR}"
    echo ""
    ask "¿Qué desea hacer?"
    echo "    [1] Actualizar (rebuild del worker, mantiene datos de InfluxDB)"
    echo "    [2] Reinstalar desde cero (elimina todos los datos)"
    echo "    [3] Cancelar"
    echo ""
    read -rp "    Opción [1/2/3]: " EXISTING_ACTION

    case "$EXISTING_ACTION" in
        1)
            info "Actualizando worker existente..."
            cd "$INSTALL_DIR"
            docker compose pull 2>/dev/null || true
            docker compose build --no-cache worker
            docker compose up -d
            echo ""
            ok "Worker actualizado exitosamente."
            info "Revise los logs con: docker logs ipmonitor_worker --tail 30"
            exit 0
            ;;
        2)
            warn "Eliminando instalación anterior y todos sus datos..."
            cd "$INSTALL_DIR"
            docker compose down -v 2>/dev/null || true
            cd /
            rm -rf "$INSTALL_DIR"
            ok "Instalación anterior eliminada"
            ;;
        3)
            info "Instalación cancelada."
            exit 0
            ;;
        *)
            die "Opción no válida."
            ;;
    esac
fi

# ─── 6. Collect Configuration ─────────────────────────────────────────────────
header "Configuración del worker"

echo ""
info "El administrador de IPMonitor le habrá proporcionado:"
info "  - WORKER_ID: identificador único UUID de este worker"
info "  - HMAC_SECRET: clave secreta para autenticación con el control plane"
echo ""

# WORKER_ID
if [[ -z "${WORKER_ID:-}" ]]; then
    ask "WORKER_ID (UUID proporcionado por el administrador):"
    read -rp "    > " WORKER_ID
fi
WORKER_ID="${WORKER_ID// /}"
if [[ -z "$WORKER_ID" ]]; then
    die "WORKER_ID es requerido."
fi
ok "WORKER_ID: ${WORKER_ID}"

# HMAC_SECRET
if [[ -z "${HMAC_SECRET:-}" ]]; then
    ask "HMAC_SECRET (clave secreta proporcionada por el administrador):"
    read -rsp "    > " HMAC_SECRET
    echo ""
fi
HMAC_SECRET="${HMAC_SECRET// /}"
if [[ -z "$HMAC_SECRET" ]]; then
    die "HMAC_SECRET es requerido."
fi
if [[ ${#HMAC_SECRET} -lt 32 ]]; then
    die "HMAC_SECRET debe tener al menos 32 caracteres."
fi
ok "HMAC_SECRET: configurado (${#HMAC_SECRET} caracteres)"

# CONTROL_PLANE_URL
if [[ -z "${CONTROL_PLANE_URL:-}" ]]; then
    ask "URL del control plane [default: ${CONTROL_PLANE_URL_DEFAULT}]:"
    read -rp "    > " CONTROL_PLANE_URL_INPUT
    CONTROL_PLANE_URL="${CONTROL_PLANE_URL_INPUT:-$CONTROL_PLANE_URL_DEFAULT}"
fi
CONTROL_PLANE_URL="${CONTROL_PLANE_URL%/}"  # remove trailing slash
ok "Control plane: ${CONTROL_PLANE_URL}"

# SMTP (optional)
echo ""
ask "¿Configurar notificaciones por email SMTP? (opcional) [s/N]:"
read -rp "    > " CONFIGURE_SMTP
SMTP_HOST=""
SMTP_PORT="587"
SMTP_USERNAME=""
SMTP_PASSWORD=""
SMTP_FROM=""

if [[ "${CONFIGURE_SMTP,,}" == "s" || "${CONFIGURE_SMTP,,}" == "si" || "${CONFIGURE_SMTP,,}" == "y" ]]; then
    ask "SMTP Host (ej: smtp.gmail.com):"
    read -rp "    > " SMTP_HOST
    ask "SMTP Port [587]:"
    read -rp "    > " SMTP_PORT_INPUT
    SMTP_PORT="${SMTP_PORT_INPUT:-587}"
    ask "SMTP Usuario (email):"
    read -rp "    > " SMTP_USERNAME
    ask "SMTP Password:"
    read -rsp "    > " SMTP_PASSWORD
    echo ""
    ask "Email remitente (SMTP From) [default: ${SMTP_USERNAME}]:"
    read -rp "    > " SMTP_FROM_INPUT
    SMTP_FROM="${SMTP_FROM_INPUT:-$SMTP_USERNAME}"
    ok "SMTP configurado: ${SMTP_HOST}:${SMTP_PORT} (${SMTP_USERNAME})"
else
    info "SMTP omitido. Las notificaciones por email no estarán activas."
fi

# Modo de conectividad: push + pull vs pull solo
echo ""
ask "¿Este servidor tiene IP pública fija? [s/N]"
info "  Sí → el control plane enviará señales de reload instantáneo (puerto 8081)."
info "  No → los cambios se propagan en ≤30 segundos vía pull. No requiere IP pública."
read -rp "    > " HAS_PUBLIC_IP

EXPOSE_RELOAD_PORT=false
if [[ "${HAS_PUBLIC_IP,,}" == "s" || "${HAS_PUBLIC_IP,,}" == "si" || "${HAS_PUBLIC_IP,,}" == "y" ]]; then
    EXPOSE_RELOAD_PORT=true
    ok "Modo push+pull: puerto 8081 expuesto para señales de reload instantáneo"
else
    info "Modo pull-only (≤30s). Puerto 8081 no expuesto."
fi

# ─── 7. Generate InfluxDB Credentials ────────────────────────────────────────
header "Configurando credenciales de InfluxDB"

generate_secret() {
    if command -v openssl &>/dev/null; then
        openssl rand -hex 32
    else
        cat /proc/sys/kernel/random/uuid /proc/sys/kernel/random/uuid | tr -d '-\n' | head -c 64
    fi
}

INFLUX_ADMIN_USER="admin"
INFLUX_ADMIN_PASSWORD=$(generate_secret | head -c 32)

# INFLUX_TOKEN puede ser pre-provisto por el admin desde el panel de IPMonitor.
# Si no viene en el entorno, se genera uno nuevo (el admin deberá actualizarlo manualmente).
if [[ -n "${INFLUX_TOKEN:-}" ]]; then
    ok "Usando INFLUX_TOKEN pre-configurado por el administrador"
    ADMIN_MUST_UPDATE_TOKEN=false
else
    INFLUX_TOKEN=$(generate_secret)
    warn "INFLUX_TOKEN generado localmente. El administrador deberá actualizar"
    warn "el campo InfluxDB Token en el panel de Worker Nodes con el token que"
    warn "se mostrará al final de esta instalación."
    ADMIN_MUST_UPDATE_TOKEN=true
fi

# ─── 8. Create Installation Directory ────────────────────────────────────────
header "Preparando directorio de instalación"

mkdir -p "$INSTALL_DIR"
chmod 750 "$INSTALL_DIR"
ok "Directorio creado: ${INSTALL_DIR}"

# ─── 9. Write docker-compose.yml ─────────────────────────────────────────────
header "Escribiendo configuración Docker"

cat > "${INSTALL_DIR}/docker-compose.yml" << 'COMPOSE_EOF'
name: ipmonitor-worker

services:
  influxdb:
    image: influxdb:2.7-alpine
    container_name: ipmonitor_influxdb
    restart: unless-stopped
    volumes:
      - influxdb_data:/var/lib/influxdb2
      - influxdb_config:/etc/influxdb2
    environment:
      DOCKER_INFLUXDB_INIT_MODE: setup
      DOCKER_INFLUXDB_INIT_USERNAME: ${INFLUX_ADMIN_USER}
      DOCKER_INFLUXDB_INIT_PASSWORD: ${INFLUX_ADMIN_PASSWORD}
      DOCKER_INFLUXDB_INIT_ORG: ${INFLUX_ORG}
      DOCKER_INFLUXDB_INIT_BUCKET: ${INFLUX_BUCKET}
      DOCKER_INFLUXDB_INIT_ADMIN_TOKEN: ${INFLUX_TOKEN}
    healthcheck:
      test: ["CMD", "curl", "-sf", "http://localhost:8086/ping"]
      interval: 10s
      timeout: 5s
      retries: 12
      start_period: 30s

  worker:
    build:
      context: https://github.com/p-vic/monitor_data_server.git#master:go-monitoring-worker
    image: ipmonitor-worker:local
    container_name: ipmonitor_worker
    restart: unless-stopped
    cap_add:
      - NET_RAW
#PORTS_PLACEHOLDER
    depends_on:
      influxdb:
        condition: service_healthy
    environment:
      WORKER_ID: ${WORKER_ID}
      CONTROL_PLANE_URL: ${CONTROL_PLANE_URL}
      HMAC_SECRET: ${HMAC_SECRET}
      INFLUX_URL: http://influxdb:8086
      INFLUX_TOKEN: ${INFLUX_TOKEN}
      INFLUX_ORG: ${INFLUX_ORG}
      INFLUX_BUCKET: ${INFLUX_BUCKET}
      SMTP_HOST: ${SMTP_HOST:-}
      SMTP_PORT: ${SMTP_PORT:-587}
      SMTP_USERNAME: ${SMTP_USERNAME:-}
      SMTP_PASSWORD: ${SMTP_PASSWORD:-}
      SMTP_FROM: ${SMTP_FROM:-}
      TELEGRAM_BOT_TOKEN: ${TELEGRAM_BOT_TOKEN:-}

volumes:
  influxdb_data:
    driver: local
  influxdb_config:
    driver: local

networks:
  default:
    driver: bridge
    driver_opts:
      com.docker.network.driver.mtu: "1500"
COMPOSE_EOF

if [[ "$EXPOSE_RELOAD_PORT" == "true" ]]; then
    sed -i 's/#PORTS_PLACEHOLDER/    ports:\n      - "8081:8081"/' "${INSTALL_DIR}/docker-compose.yml"
else
    sed -i '/#PORTS_PLACEHOLDER/d' "${INSTALL_DIR}/docker-compose.yml"
fi
ok "docker-compose.yml escrito"

# ─── 10. Write .env ───────────────────────────────────────────────────────────
cat > "${INSTALL_DIR}/.env" << ENV_EOF
# IPMonitor Worker — Environment Configuration
# Generated: $(date -u '+%Y-%m-%d %H:%M:%S UTC')
# DO NOT share this file — contains secrets.

# Worker identity (provided by IPMonitor admin)
WORKER_ID=${WORKER_ID}
CONTROL_PLANE_URL=${CONTROL_PLANE_URL}
HMAC_SECRET=${HMAC_SECRET}

# InfluxDB (auto-generated — do not modify after first start)
INFLUX_ADMIN_USER=${INFLUX_ADMIN_USER}
INFLUX_ADMIN_PASSWORD=${INFLUX_ADMIN_PASSWORD}
INFLUX_TOKEN=${INFLUX_TOKEN}
INFLUX_ORG=${INFLUX_ORG}
INFLUX_BUCKET=${INFLUX_BUCKET}

# SMTP (optional — leave empty to disable email alerts)
SMTP_HOST=${SMTP_HOST}
SMTP_PORT=${SMTP_PORT}
SMTP_USERNAME=${SMTP_USERNAME}
SMTP_PASSWORD=${SMTP_PASSWORD}
SMTP_FROM=${SMTP_FROM}

# Telegram (optional — leave empty to disable)
TELEGRAM_BOT_TOKEN=
ENV_EOF

chmod 600 "${INSTALL_DIR}/.env"
ok ".env escrito con permisos 600"

# ─── 11. Build & Start Services ───────────────────────────────────────────────
header "Construyendo e iniciando servicios"

echo ""
warn "Verificando acceso al repositorio público de GitHub..."
if ! curl -fsSL --head "https://github.com/p-vic/monitor_data_server" &>/dev/null; then
    die "No se pudo acceder a github.com/p-vic/monitor_data_server\n\n" \
        "     Posibles causas:\n" \
        "     - El repositorio aún no es público\n" \
        "     - Sin acceso a internet\n\n" \
        "     Contacte al administrador de IPMonitor."
fi
ok "Repositorio accesible"

echo ""
if [[ "$IS_RASPI" == "true" ]]; then
    warn "Raspberry Pi detectado — el build tomará entre 5 y 15 minutos."
    warn "No interrumpa el proceso."
else
    info "Construyendo worker Go (primera vez: 2-5 minutos)..."
fi
echo ""

cd "$INSTALL_DIR"
docker compose build --progress=plain 2>&1 | \
    grep -E "^(Step|---> |Successfully|#[0-9])" || true

echo ""
info "Iniciando contenedores..."
docker compose up -d

# ─── 12. Wait for InfluxDB ────────────────────────────────────────────────────
header "Esperando que InfluxDB esté listo"

INFLUX_WAIT=0
INFLUX_MAX=90
until docker exec ipmonitor_influxdb curl -sf http://localhost:8086/ping &>/dev/null; do
    if [[ $INFLUX_WAIT -ge $INFLUX_MAX ]]; then
        die "InfluxDB no respondió después de ${INFLUX_MAX}s.\n     Revise: docker logs ipmonitor_influxdb --tail 30"
    fi
    echo -ne "    Esperando InfluxDB... ${INFLUX_WAIT}s\r"
    sleep 5
    INFLUX_WAIT=$((INFLUX_WAIT + 5))
done
echo ""
ok "InfluxDB listo"

# ─── 13. Wait for Worker Sync ─────────────────────────────────────────────────
header "Verificando conexión al control plane"

info "Esperando primer sync del worker con ${CONTROL_PLANE_URL}..."
SYNC_WAIT=0
SYNC_MAX=60
SYNC_OK=false

while [[ $SYNC_WAIT -lt $SYNC_MAX ]]; do
    if docker logs ipmonitor_worker 2>&1 | grep -q "Sync OK"; then
        SYNC_OK=true
        break
    fi
    echo -ne "    Esperando sync... ${SYNC_WAIT}s\r"
    sleep 5
    SYNC_WAIT=$((SYNC_WAIT + 5))
done
echo ""

if [[ "$SYNC_OK" == "true" ]]; then
    ok "Worker sincronizado con el control plane exitosamente"
else
    warn "No se detectó sync en ${SYNC_MAX}s. El worker seguirá reintentando cada 30s."
    warn "Verifique con: docker logs ipmonitor_worker --tail 30"
    warn "Causas posibles: WORKER_ID o HMAC_SECRET incorrectos, o el worker no"
    warn "está registrado aún en el panel de administración de IPMonitor."
fi

# ─── 14. Summary ──────────────────────────────────────────────────────────────
header "Instalación completada"

echo ""
echo -e "${BOLD}${GREEN}  ╔══════════════════════════════════════════════════╗"
echo "  ║         Worker instalado correctamente           ║"
echo -e "  ╚══════════════════════════════════════════════════╝${NC}"
echo ""
echo -e "${BOLD}  Resumen:${NC}"
echo "  ├─ Directorio : ${INSTALL_DIR}"
echo "  ├─ Worker ID  : ${WORKER_ID}"
echo "  ├─ Control    : ${CONTROL_PLANE_URL}"
echo "  ├─ InfluxDB   : http://localhost:8086 (solo acceso local)"
if [[ "$SMTP_HOST" != "" ]]; then
echo "  └─ SMTP       : ${SMTP_HOST}:${SMTP_PORT}"
else
echo "  └─ SMTP       : no configurado"
fi
echo ""
echo -e "${BOLD}  Modo de conectividad:${NC}"
if [[ "$EXPOSE_RELOAD_PORT" == "true" ]]; then
    echo -e "  ├─ ${GREEN}Push + Pull${NC}  — reload instantáneo cuando el admin cambia targets"
    echo "  ├─ Puerto 8081 expuesto — asegúrese de abrirlo en el firewall:"
    if [[ "$PKG_FAMILY" == "debian" ]]; then
        echo "  │    sudo ufw allow 8081/tcp && sudo ufw reload"
    else
        echo "  │    sudo firewall-cmd --permanent --add-port=8081/tcp && sudo firewall-cmd --reload"
    fi
    echo "  └─ Informe la IP pública al admin para configurar el campo 'Go API Endpoint'"
    echo "     en el panel Worker Nodes: http://<esta-ip>:8081"
else
    echo -e "  └─ ${CYAN}Pull-only (≤30s)${NC} — sin necesidad de IP pública ni port forwarding"
fi
echo ""
echo -e "${BOLD}  Comandos útiles:${NC}"
echo "  ├─ Ver logs del worker   : docker logs ipmonitor_worker -f"
echo "  ├─ Ver logs de InfluxDB  : docker logs ipmonitor_influxdb -f"
echo "  ├─ Estado de contenedores: docker compose -f ${INSTALL_DIR}/docker-compose.yml ps"
echo "  ├─ Reiniciar worker      : docker compose -f ${INSTALL_DIR}/docker-compose.yml restart worker"
echo "  └─ Detener todo          : docker compose -f ${INSTALL_DIR}/docker-compose.yml down"
echo ""
# Detect public IP to show admin
PUBLIC_IP=$(curl -fsSL --max-time 5 https://api.ipify.org 2>/dev/null || echo "no detectada")

if [[ "$SYNC_OK" == "true" ]]; then
    echo -e "  ${GREEN}${BOLD}El worker ya está monitoreando. Verifique el dashboard en:${NC}"
else
    echo -e "  ${YELLOW}${BOLD}Próximo paso: verifique que el worker aparezca activo en:${NC}"
fi
echo -e "  ${BOLD}  ${CONTROL_PLANE_URL}/dashboard${NC}"
echo ""

# If token was self-generated, show it prominently so admin can update the panel
if [[ "${ADMIN_MUST_UPDATE_TOKEN:-false}" == "true" ]]; then
    echo -e "${BOLD}${YELLOW}  ┌─────────────────────────────────────────────────────────────┐"
    echo "  │  ACCIÓN REQUERIDA — Informe esto al administrador de IPMonitor│"
    echo "  ├─────────────────────────────────────────────────────────────┤"
    echo "  │  IP Pública del servidor : ${PUBLIC_IP}"
    echo "  │  InfluxDB Token generado : ${INFLUX_TOKEN}"
    echo "  └─────────────────────────────────────────────────────────────┘"
    echo -e "${NC}"
    echo "  El admin debe actualizar en Worker Nodes → Edit → InfluxDB Token"
    echo "  e InfluxDB URL: http://${PUBLIC_IP}:8086"
    echo ""
else
    echo "  IP pública detectada: ${PUBLIC_IP}"
    echo "  (Informe esta IP al admin para configurar InfluxDB URL si aún no lo hizo)"
    echo ""
fi
