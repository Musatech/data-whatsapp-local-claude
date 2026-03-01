#!/usr/bin/env bash
# ============================================================
# WhatsApp Local Claude - Script de Encerramento
# ============================================================
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BRIDGE_PID_FILE="$ROOT_DIR/data/.bridge.pid"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

echo ""
echo -e "${YELLOW}Encerrando WhatsApp Local Claude...${NC}"
echo ""

# ─── Para o Bridge ───
if [[ -f "$BRIDGE_PID_FILE" ]]; then
    BRIDGE_PID=$(cat "$BRIDGE_PID_FILE")
    if kill -0 "$BRIDGE_PID" 2>/dev/null; then
        echo "  Encerrando WhatsApp Bridge (PID: $BRIDGE_PID)..."
        kill "$BRIDGE_PID"
        sleep 1
        if kill -0 "$BRIDGE_PID" 2>/dev/null; then
            kill -9 "$BRIDGE_PID" 2>/dev/null || true
        fi
        echo -e "  ${GREEN}✓ Bridge encerrado${NC}"
    else
        echo -e "  ${YELLOW}⚠ Bridge não estava rodando${NC}"
    fi
    rm -f "$BRIDGE_PID_FILE"
else
    echo -e "  ${YELLOW}⚠ Nenhum processo Bridge encontrado${NC}"
fi

# ─── Para processos Python do MCP se houver ───
MCP_PIDS=$(pgrep -f "mcp-server/server.py" 2>/dev/null || true)
if [[ -n "$MCP_PIDS" ]]; then
    echo "  Encerrando MCP Server..."
    echo "$MCP_PIDS" | xargs kill 2>/dev/null || true
    echo -e "  ${GREEN}✓ MCP Server encerrado${NC}"
fi

echo ""
echo -e "${GREEN}Sistema encerrado.${NC}"
echo ""
