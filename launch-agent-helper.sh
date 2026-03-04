#!/usr/bin/env bash
# ============================================================
# WhatsApp Local Claude - LaunchAgent Helper
# Exibe diálogo ao login e inicia o bridge se o usuário confirmar
# ============================================================

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Aguarda o ambiente do usuário estar pronto (Dock, etc.)
sleep 5

# Mostra diálogo de confirmação via AppleScript
RESULT=$(osascript <<'EOF'
display dialog "Deseja iniciar o WhatsApp Bridge agora?" ¬
    buttons {"Não", "Sim"} ¬
    default button "Sim" ¬
    with title "WhatsApp Local Claude" ¬
    with icon note
button returned of result
EOF
)

if [[ "$RESULT" == "Sim" ]]; then
    "$ROOT_DIR/start.sh"
fi
