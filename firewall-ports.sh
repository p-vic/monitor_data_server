#!/bin/bash
# Bloquea el acceso desde internet a los puertos publicados de ipmonitor
# (8083 = API worker 3, 8085/8086/8087 = InfluxDB w1/w2/w3) mediante DOCKER-USER.
#
# Solo filtra el tráfico que entra por la interfaz pública: el control plane
# llega a estos puertos por la red bridge de Docker y no se ve afectado.
#
# En DOCKER-USER el DNAT ya se aplicó (dport = puerto del contenedor), por eso
# se compara contra el puerto original con conntrack --ctorigdstport.
#
# Uso: ./firewall-ports.sh [apply|remove|status]
#   IFACE=eth0 ./firewall-ports.sh apply   (por defecto: interfaz de la ruta default)

set -euo pipefail

ACTION=${1:-status}
COMMENT="ipmonitor-block-public"
PORT_SPECS=("8083" "8085:8087")

IFACE=${IFACE:-$(ip route show default | awk '{print $5; exit}')}
if [[ -z "$IFACE" ]]; then
  echo "No se pudo detectar la interfaz pública; use IFACE=..." >&2
  exit 1
fi

rule_args() {
  echo "-i $IFACE -p tcp -m conntrack --ctorigdstport $1 --ctdir ORIGINAL -m comment --comment $COMMENT -j DROP"
}

# Docker crea DOCKER-USER al arrancar; en el boot puede tardar unos segundos.
wait_for_chain() {
  local bin=$1
  for _ in $(seq 1 60); do
    "$bin" -n -L DOCKER-USER &>/dev/null && return 0
    sleep 1
  done
  return 1
}

apply_rules() {
  local bin=$1
  for spec in "${PORT_SPECS[@]}"; do
    # shellcheck disable=SC2046
    if ! "$bin" -C DOCKER-USER $(rule_args "$spec") 2>/dev/null; then
      # shellcheck disable=SC2046
      "$bin" -I DOCKER-USER 1 $(rule_args "$spec")
      echo "[$bin] añadida: $IFACE tcp $spec -> DROP"
    else
      echo "[$bin] ya existe: $IFACE tcp $spec -> DROP"
    fi
  done
}

remove_rules() {
  local bin=$1
  for spec in "${PORT_SPECS[@]}"; do
    # shellcheck disable=SC2046
    while "$bin" -C DOCKER-USER $(rule_args "$spec") 2>/dev/null; do
      # shellcheck disable=SC2046
      "$bin" -D DOCKER-USER $(rule_args "$spec")
      echo "[$bin] eliminada: $IFACE tcp $spec"
    done
  done
}

case "$ACTION" in
  apply)
    wait_for_chain iptables || { echo "DOCKER-USER no existe (¿Docker corriendo?)" >&2; exit 1; }
    apply_rules iptables
    # Con ip6tables activo en Docker, los puertos también se publican por IPv6.
    if command -v ip6tables &>/dev/null && ip6tables -n -L DOCKER-USER &>/dev/null; then
      apply_rules ip6tables
    fi
    ;;
  remove)
    iptables -n -L DOCKER-USER &>/dev/null && remove_rules iptables
    if command -v ip6tables &>/dev/null && ip6tables -n -L DOCKER-USER &>/dev/null; then
      remove_rules ip6tables
    fi
    ;;
  status)
    echo "Interfaz pública: $IFACE"
    echo "--- iptables DOCKER-USER"
    iptables -n -v -L DOCKER-USER --line-numbers
    if command -v ip6tables &>/dev/null && ip6tables -n -L DOCKER-USER &>/dev/null; then
      echo "--- ip6tables DOCKER-USER"
      ip6tables -n -v -L DOCKER-USER --line-numbers
    fi
    ;;
  *)
    echo "Uso: $0 [apply|remove|status]"
    exit 1
    ;;
esac
