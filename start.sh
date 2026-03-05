#!/usr/bin/env bash
# ============================================================
# WhatsApp Local Claude - Script de Inicialização
# ============================================================
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BRIDGE_PID_FILE="$ROOT_DIR/data/.bridge.pid"
LOG_DIR="$ROOT_DIR/data/logs"

# Cores
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m'

mkdir -p "$LOG_DIR"

echo ""
echo -e "${BLUE}=====================================${NC}"
echo -e "${BLUE}  WhatsApp Local Claude - Iniciando  ${NC}"
echo -e "${BLUE}=====================================${NC}"
echo ""

# ─── Verifica se o bridge já está rodando ───
# Valida o PID E confirma que pertence ao nosso binário
# (evita falso positivo após reboot, quando o macOS reutiliza PIDs)
BRIDGE_BIN_NAME="whatsapp-bridge"
if [[ -f "$BRIDGE_PID_FILE" ]]; then
    OLD_PID=$(cat "$BRIDGE_PID_FILE")
    if kill -0 "$OLD_PID" 2>/dev/null && \
       ps -p "$OLD_PID" -o comm= 2>/dev/null | grep -q "$BRIDGE_BIN_NAME"; then
        echo -e "${YELLOW}⚠ WhatsApp Bridge já está rodando (PID: $OLD_PID)${NC}"
        echo "  Use ./stop.sh para encerrar antes de reiniciar."
        exit 0
    else
        rm -f "$BRIDGE_PID_FILE"
    fi
fi

# ─── Verifica se o bridge foi compilado ───
BRIDGE_BIN="$ROOT_DIR/whatsapp-bridge/whatsapp-bridge"
if [[ ! -f "$BRIDGE_BIN" ]]; then
    echo -e "${RED}✗ Bridge não compilado. Execute ./install.sh primeiro.${NC}"
    exit 1
fi

# ─── Verifica se o ambiente Python existe ───
PYTHON_PATH="$ROOT_DIR/mcp-server/.venv/bin/python"
if [[ ! -f "$PYTHON_PATH" ]]; then
    echo -e "${RED}✗ Ambiente Python não configurado. Execute ./install.sh primeiro.${NC}"
    exit 1
fi

# ─── Carrega .env ───
if [[ -f "$ROOT_DIR/.env" ]]; then
    set -a
    # shellcheck disable=SC1091
    source "$ROOT_DIR/.env"
    set +a
fi

# ─── Inicia o WhatsApp Bridge ───
echo -e "${YELLOW}▶ Iniciando WhatsApp Bridge...${NC}"
BRIDGE_LOG="$LOG_DIR/bridge.log"

cd "$ROOT_DIR/whatsapp-bridge"
"$BRIDGE_BIN" >> "$BRIDGE_LOG" 2>&1 &
BRIDGE_PID=$!
echo $BRIDGE_PID > "$BRIDGE_PID_FILE"
cd "$ROOT_DIR"

echo -e "  ${GREEN}✓ Bridge iniciado (PID: $BRIDGE_PID)${NC}"
echo -e "  ${BLUE}ℹ Logs em: $BRIDGE_LOG${NC}"

# ─── Verifica se está rodando e mostra QR se necessário ───
sleep 1
if ! kill -0 "$BRIDGE_PID" 2>/dev/null; then
    echo -e "${RED}✗ Bridge falhou ao iniciar. Verifique os logs:${NC}"
    echo "  tail -n +1 -f $BRIDGE_LOG"
    rm -f "$BRIDGE_PID_FILE"
    exit 1
fi

echo ""
echo -e "${GREEN}=====================================${NC}"
echo -e "${GREEN}  Sistema iniciado com sucesso!       ${NC}"
echo -e "${GREEN}=====================================${NC}"
echo ""
echo "  O WhatsApp Bridge está rodando em segundo plano."
echo ""
echo "  Se for a primeira vez, o QR Code será exibido nos logs:"
echo -e "  ${BLUE}tail -n +1 -f $BRIDGE_LOG${NC}"
echo ""
echo "  Para parar: ${YELLOW}./stop.sh${NC}"
echo ""
echo -e "${YELLOW}Nota:${NC} O MCP Server é iniciado automaticamente pelo"
echo "      Claude Desktop quando você usar as ferramentas WhatsApp."
echo ""
