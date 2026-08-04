#!/bin/bash
# =============================================================================
# IPMonitor Agent — Automated Installation Script
# =============================================================================
# Supports: Ubuntu 20.04+, Debian 11+, Raspberry Pi OS (64-bit),
#           CentOS 8+, Rocky Linux 8+, AlmaLinux 8+
#
# Usage (one-liner provided by admin panel):
#   curl -fsSL https://ipmonitor.yaurima.com/install-agent.sh | \
#     sudo INSTALL_TOKEN=<token> bash
#
# Non-interactive with all options:
#   sudo INSTALL_TOKEN=<token> CP_URL=https://... SMTP_HOST=smtp.gmail.com \
#     SMTP_PORT=587 SMTP_USERNAME=you@gmail.com SMTP_PASSWORD=xxx bash install-agent.sh
# =============================================================================
set -euo pipefail

# When run via `curl | sudo ... bash` (the documented one-liner above), stdin is
# consumed by the script source itself — every `read -rp` below would otherwise
# see EOF immediately instead of the operator's input. Re-point stdin at the
# controlling terminal so prompts (SMTP setup, the reinstall menu) actually work.
# If there's no tty at all (fully scripted/CI invocation), this is skipped and
# reads fall back to their existing defaults, same as before this fix.
if [[ ! -t 0 ]] && [[ -r /dev/tty ]]; then
    exec < /dev/tty
fi

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
BINARY_PATH="/usr/local/bin/ipmonitor-agent"
CONFIG_DIR="/etc/ipmonitor-agent"
ENV_FILE="${CONFIG_DIR}/env"
CP_URL_DEFAULT="https://ipmonitor.yaurima.com"
# Binary downloads are served from the CP: /downloads/ipmonitor-agent-linux-<arch>
DOWNLOAD_BASE="${DOWNLOAD_BASE:-}"  # defaults to ${CP_URL}/downloads below

# ─── Banner ───────────────────────────────────────────────────────────────────
echo ""
echo -e "${BOLD}${BLUE}"
echo "  ╔══════════════════════════════════════════════════╗"
echo "  ║         IPMonitor Agent — Installer              ║"
echo "  ║         ipmonitor.yaurima.com                    ║"
echo "  ╚══════════════════════════════════════════════════╝"
echo -e "${NC}"

# ─── 1. Root check ────────────────────────────────────────────────────────────
header "Verificando permisos"
if [[ $EUID -ne 0 ]]; then
    die "Este script debe ejecutarse como root.\n     Ejecute: sudo bash $0"
fi
ok "Ejecutando como root"

# ─── 2. Systemd check ─────────────────────────────────────────────────────────
if ! command -v systemctl &>/dev/null; then
    die "systemd no encontrado. El agente requiere systemd para instalarse como servicio."
fi
ok "systemd disponible"

# ─── 3. OS & Architecture Detection ──────────────────────────────────────────
header "Detectando sistema operativo"

ARCH=$(uname -m)
case "$ARCH" in
    x86_64)  ARCH_LABEL="amd64" ;;
    aarch64) ARCH_LABEL="arm64" ;;
    armv7l)  die "Arquitectura armv7 (32-bit) no soportada. Use un sistema de 64 bits." ;;
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
[[ "$IS_RASPI" == "true" ]] && ok "Dispositivo: Raspberry Pi detectado"

# ─── 4. curl check ────────────────────────────────────────────────────────────
header "Verificando dependencias"

if ! command -v curl &>/dev/null; then
    info "Instalando curl..."
    case "${ID:-}" in
        ubuntu|debian|raspbian|linuxmint)
            apt-get update -qq && apt-get install -y -qq curl ;;
        centos|rhel|rocky|almalinux|fedora)
            yum install -y curl 2>/dev/null || dnf install -y curl ;;
        *)
            ID_LIKE="${ID_LIKE:-}"
            if echo "$ID_LIKE" | grep -qE "debian|ubuntu"; then
                apt-get update -qq && apt-get install -y -qq curl
            elif echo "$ID_LIKE" | grep -qE "rhel|centos|fedora"; then
                yum install -y curl 2>/dev/null || dnf install -y curl
            else
                die "No se pudo instalar curl. Instálelo manualmente e intente de nuevo."
            fi ;;
    esac
fi
ok "curl disponible"

# ─── 5. Collect configuration ─────────────────────────────────────────────────
header "Configuración del agente"

# INSTALL_TOKEN (required)
if [[ -z "${INSTALL_TOKEN:-}" ]]; then
    ask "INSTALL_TOKEN (proporcionado por el administrador de IPMonitor):"
    read -rp "    > " INSTALL_TOKEN
fi
INSTALL_TOKEN="${INSTALL_TOKEN// /}"
[[ -z "$INSTALL_TOKEN" ]] && die "INSTALL_TOKEN es requerido."
ok "INSTALL_TOKEN: configurado"

# CP_URL
if [[ -z "${CP_URL:-}" ]]; then
    ask "URL del Control Plane [default: ${CP_URL_DEFAULT}]:"
    read -rp "    > " CP_URL_INPUT
    CP_URL="${CP_URL_INPUT:-$CP_URL_DEFAULT}"
fi
CP_URL="${CP_URL%/}"
ok "Control Plane: ${CP_URL}"

# Set download base from CP_URL if not overridden
DOWNLOAD_BASE="${DOWNLOAD_BASE:-${CP_URL}/downloads}"

# SMTP (optional — can be pre-set via env vars for non-interactive installs)
SMTP_HOST="${SMTP_HOST:-}"
SMTP_PORT="${SMTP_PORT:-587}"
SMTP_USERNAME="${SMTP_USERNAME:-}"
SMTP_PASSWORD="${SMTP_PASSWORD:-}"
SMTP_FROM="${SMTP_FROM:-}"
TELEGRAM_BOT_TOKEN="${TELEGRAM_BOT_TOKEN:-}"

# Only prompt if not already set via env
if [[ -z "$SMTP_HOST" ]]; then
    echo ""
    ask "¿Configurar notificaciones por email SMTP? (opcional) [s/N]:"
    read -rp "    > " CONFIGURE_SMTP

    if [[ "${CONFIGURE_SMTP,,}" =~ ^(s|si|y|yes)$ ]]; then
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
        ask "Email remitente [default: ${SMTP_USERNAME}]:"
        read -rp "    > " SMTP_FROM_INPUT
        SMTP_FROM="${SMTP_FROM_INPUT:-$SMTP_USERNAME}"
        ok "SMTP configurado: ${SMTP_HOST}:${SMTP_PORT}"
    else
        info "SMTP omitido. Las notificaciones por email no estarán activas."
    fi
fi

# ─── 6. Check existing installation ──────────────────────────────────────────
header "Verificando instalación previa"

CREDS_FILE="${CONFIG_DIR}/credentials.json"
if [[ -f "$CREDS_FILE" ]]; then
    warn "Ya existe una instalación en ${CONFIG_DIR}"
    echo ""
    ask "¿Qué desea hacer?"
    echo "    [1] Reinstalar (re-registrar con nuevo token, mantiene config SMTP)"
    echo "    [2] Solo actualizar el binario (mantiene credenciales actuales)"
    echo "    [3] Cancelar"
    echo ""
    read -rp "    Opción [1/2/3]: " EXISTING_ACTION

    case "$EXISTING_ACTION" in
        1)
            info "Se procederá con registro nuevo..."
            DO_REGISTER=true
            ;;
        2)
            info "Solo se actualizará el binario..."
            DO_REGISTER=false
            ;;
        3)
            info "Instalación cancelada."
            exit 0
            ;;
        *)
            die "Opción no válida."
            ;;
    esac
else
    DO_REGISTER=true
fi

# Stop existing service if running
if systemctl is-active --quiet ipmonitor-agent 2>/dev/null; then
    info "Deteniendo servicio existente..."
    systemctl stop ipmonitor-agent
    ok "Servicio detenido"
fi

# ─── 7. Download binary ───────────────────────────────────────────────────────
header "Descargando binario del agente"

BINARY_NAME="ipmonitor-agent-linux-${ARCH_LABEL}"
DOWNLOAD_URL="${DOWNLOAD_BASE}/${BINARY_NAME}"

info "Descargando desde ${DOWNLOAD_URL}..."
TMP_BINARY=$(mktemp)

if ! curl -fsSL --progress-bar "${DOWNLOAD_URL}" -o "$TMP_BINARY"; then
    rm -f "$TMP_BINARY"
    die "No se pudo descargar el binario desde ${DOWNLOAD_URL}\n" \
        "     Verifique que el Control Plane esté accesible y que el archivo exista."
fi

chmod +x "$TMP_BINARY"

# Quick sanity check — verify it's a valid ELF binary
if ! file "$TMP_BINARY" | grep -q "ELF"; then
    rm -f "$TMP_BINARY"
    die "El archivo descargado no es un binario válido. Contacte al administrador."
fi

mv "$TMP_BINARY" "$BINARY_PATH"
ok "Binario instalado en ${BINARY_PATH}"

AGENT_VERSION=$(ipmonitor-agent version 2>/dev/null || echo "desconocida")
ok "Versión: ${AGENT_VERSION}"

# ─── 8. Write env file ────────────────────────────────────────────────────────
header "Escribiendo configuración"

mkdir -p "$CONFIG_DIR"
chmod 700 "$CONFIG_DIR"

cat > "$ENV_FILE" << ENV_EOF
# IPMonitor Agent — Environment Configuration
# Generated: $(date -u '+%Y-%m-%d %H:%M:%S UTC')
# DO NOT share this file — contains secrets.

# SMTP (optional — leave empty to disable email alerts)
SMTP_HOST=${SMTP_HOST}
SMTP_PORT=${SMTP_PORT}
SMTP_USERNAME=${SMTP_USERNAME}
SMTP_PASSWORD=${SMTP_PASSWORD}
SMTP_FROM=${SMTP_FROM}

# Telegram (optional — leave empty to disable)
TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN}
ENV_EOF

chmod 600 "$ENV_FILE"
ok "Archivo de entorno escrito: ${ENV_FILE}"

# ─── 9. Register agent ────────────────────────────────────────────────────────
if [[ "$DO_REGISTER" == "true" ]]; then
    header "Registrando agente con el Control Plane"

    info "Enviando solicitud de registro a ${CP_URL}..."
    if ! ipmonitor-agent register --token "$INSTALL_TOKEN" --url "$CP_URL"; then
        die "El registro falló. Verifique que el token sea válido y no haya expirado.\n" \
            "     El administrador puede generar un nuevo token desde el panel."
    fi
    ok "Agente registrado exitosamente"
    ok "Credenciales guardadas en: ${CREDS_FILE}"
else
    ok "Usando credenciales existentes en ${CREDS_FILE}"
fi

# ─── 10. Install & start service ──────────────────────────────────────────────
header "Instalando servicio systemd"

ipmonitor-agent service install
ok "Servicio instalado y habilitado"

ipmonitor-agent service start
ok "Servicio iniciado"

# ─── 11. Verify agent is running ──────────────────────────────────────────────
header "Verificando funcionamiento"

sleep 3  # give the service a moment to start

if systemctl is-active --quiet ipmonitor-agent; then
    ok "Servicio ipmonitor-agent: activo"
else
    warn "El servicio no parece estar corriendo. Revise los logs:"
    warn "  journalctl -u ipmonitor-agent -n 30 --no-pager"
fi

# Wait for first config sync
info "Esperando primer sync con el Control Plane..."
SYNC_WAIT=0
SYNC_MAX=30
SYNC_OK=false

while [[ $SYNC_WAIT -lt $SYNC_MAX ]]; do
    if journalctl -u ipmonitor-agent --no-pager -n 50 2>/dev/null | grep -q "sync OK"; then
        SYNC_OK=true
        break
    fi
    echo -ne "    Esperando sync... ${SYNC_WAIT}s\r"
    sleep 3
    SYNC_WAIT=$((SYNC_WAIT + 3))
done
echo ""

if [[ "$SYNC_OK" == "true" ]]; then
    ok "Agente sincronizado con el Control Plane"
else
    warn "No se detectó sync en ${SYNC_MAX}s. El agente reintentará automáticamente."
    warn "Verifique con: journalctl -u ipmonitor-agent -f"
fi

# ─── 12. Summary ──────────────────────────────────────────────────────────────
header "Instalación completada"

PUBLIC_IP=$(curl -fsSL --max-time 5 https://api.ipify.org 2>/dev/null || echo "no detectada")

echo ""
echo -e "${BOLD}${GREEN}  ╔══════════════════════════════════════════════════╗"
echo "  ║         Agente instalado correctamente           ║"
echo -e "  ╚══════════════════════════════════════════════════╝${NC}"
echo ""
echo -e "${BOLD}  Resumen:${NC}"
echo "  ├─ Binario      : ${BINARY_PATH}"
echo "  ├─ Config       : ${CONFIG_DIR}"
echo "  ├─ Control      : ${CP_URL}"
echo "  ├─ IP pública   : ${PUBLIC_IP}"
if [[ -n "$SMTP_HOST" ]]; then
echo "  └─ SMTP         : ${SMTP_HOST}:${SMTP_PORT}"
else
echo "  └─ SMTP         : no configurado"
fi
echo ""
echo -e "${BOLD}  Comandos útiles:${NC}"
echo "  ├─ Ver logs en vivo   : journalctl -u ipmonitor-agent -f"
echo "  ├─ Estado del servicio: systemctl status ipmonitor-agent"
echo "  ├─ Reiniciar          : ipmonitor-agent service stop && ipmonitor-agent service start"
echo "  └─ Desinstalar        : ipmonitor-agent service uninstall"
echo ""
if [[ "$SYNC_OK" == "true" ]]; then
    echo -e "  ${GREEN}${BOLD}El agente ya está monitoreando. Verifíquelo en el panel:${NC}"
else
    echo -e "  ${YELLOW}${BOLD}Próximo paso: verifique que el agente aparezca activo en:${NC}"
fi
echo -e "  ${BOLD}  ${CP_URL}/dashboard${NC}"
echo ""
