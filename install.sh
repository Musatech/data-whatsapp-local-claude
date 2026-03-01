#!/usr/bin/env bash
# ============================================================
# WhatsApp Local Claude - Script de Instalação para macOS
# ============================================================
set -euo pipefail

# Cores
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Diretório raiz do projeto
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

print_header() {
    echo ""
    echo -e "${BLUE}======================================${NC}"
    echo -e "${BLUE}  WhatsApp Local Claude - Instalação  ${NC}"
    echo -e "${BLUE}======================================${NC}"
    echo ""
}

print_step() {
    echo -e "\n${YELLOW}▶ $1${NC}"
}

print_ok() {
    echo -e "  ${GREEN}✓ $1${NC}"
}

print_error() {
    echo -e "  ${RED}✗ $1${NC}"
}

print_info() {
    echo -e "  ${BLUE}ℹ $1${NC}"
}

check_macos() {
    if [[ "$(uname)" != "Darwin" ]]; then
        print_error "Este script é apenas para macOS."
        exit 1
    fi
    print_ok "macOS detectado"
}

check_homebrew() {
    print_step "Verificando Homebrew..."
    if ! command -v brew &>/dev/null; then
        print_info "Homebrew não encontrado. Instalando..."
        /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
        # Adiciona brew ao PATH para Apple Silicon
        if [[ -f /opt/homebrew/bin/brew ]]; then
            eval "$(/opt/homebrew/bin/brew shellenv)"
        fi
    fi
    print_ok "Homebrew: $(brew --version | head -1)"
}

check_xcode_tools() {
    print_step "Verificando Xcode Command Line Tools..."
    if ! xcode-select -p &>/dev/null; then
        print_info "Instalando Xcode Command Line Tools..."
        xcode-select --install
        echo "  Aguarde a instalação e pressione Enter para continuar..."
        read -r
    fi
    print_ok "Xcode CLT instalado"
}

install_go() {
    print_step "Verificando Go..."
    if ! command -v go &>/dev/null; then
        print_info "Instalando Go via Homebrew..."
        brew install go
    fi
    GO_VERSION=$(go version | awk '{print $3}')
    print_ok "Go: $GO_VERSION"
}

install_python() {
    print_step "Verificando Python 3..."
    if ! command -v python3 &>/dev/null; then
        print_info "Instalando Python via Homebrew..."
        brew install python@3.11
    fi
    PYTHON_VERSION=$(python3 --version)
    print_ok "Python: $PYTHON_VERSION"
}

install_ffmpeg() {
    print_step "Verificando ffmpeg..."
    if ! command -v ffmpeg &>/dev/null; then
        print_info "Instalando ffmpeg via Homebrew..."
        brew install ffmpeg
    fi
    print_ok "ffmpeg: $(ffmpeg -version 2>&1 | head -1)"
}

setup_env() {
    print_step "Configurando variáveis de ambiente..."
    if [[ ! -f "$ROOT_DIR/.env" ]]; then
        cp "$ROOT_DIR/.env.example" "$ROOT_DIR/.env"
        print_ok ".env criado a partir do .env.example"
        print_info "Edite $ROOT_DIR/.env para personalizar as configurações"
    else
        print_ok ".env já existe"
    fi
}

setup_directories() {
    print_step "Criando diretórios de dados..."
    # Carrega DATA_DIR do .env
    if [[ -f "$ROOT_DIR/.env" ]]; then
        # shellcheck disable=SC1091
        source <(grep -E '^[A-Z_]+=.*' "$ROOT_DIR/.env" | sed 's/^/export /')
    fi
    DATA_DIR="${DATA_DIR:-$ROOT_DIR/data}"
    MEDIA_DIR="${MEDIA_DIR:-$DATA_DIR/media}"

    mkdir -p "$DATA_DIR"
    mkdir -p "$MEDIA_DIR/audio"
    mkdir -p "$MEDIA_DIR/image"
    mkdir -p "$MEDIA_DIR/ptt"
    print_ok "Diretórios criados em: $DATA_DIR"
}

build_bridge() {
    print_step "Compilando WhatsApp Bridge (Go)..."
    cd "$ROOT_DIR/whatsapp-bridge"

    print_info "Baixando dependências Go..."
    go mod tidy

    print_info "Compilando..."
    go build -o whatsapp-bridge .

    if [[ -f "$ROOT_DIR/whatsapp-bridge/whatsapp-bridge" ]]; then
        print_ok "Bridge compilado com sucesso"
    else
        print_error "Falha ao compilar o bridge"
        exit 1
    fi
    cd "$ROOT_DIR"
}

setup_python_env() {
    print_step "Configurando ambiente Python..."
    cd "$ROOT_DIR/mcp-server"

    if [[ ! -d ".venv" ]]; then
        print_info "Criando virtual environment..."
        python3 -m venv .venv
    fi

    print_info "Ativando venv e instalando dependências..."
    # shellcheck disable=SC1091
    source .venv/bin/activate

    pip install --upgrade pip --quiet
    pip install -r requirements.txt --quiet

    print_ok "Dependências Python instaladas"
    deactivate
    cd "$ROOT_DIR"
}

install_whisper_model() {
    print_step "Baixando modelo Whisper..."

    # Carrega configuração do .env
    if [[ -f "$ROOT_DIR/.env" ]]; then
        # shellcheck disable=SC1091
        source <(grep -E '^[A-Z_]+=.*' "$ROOT_DIR/.env" | sed 's/^/export /')
    fi
    WHISPER_MODEL="${WHISPER_MODEL:-small}"

    print_info "Baixando modelo '$WHISPER_MODEL' (pode demorar na primeira vez)..."
    cd "$ROOT_DIR/mcp-server"
    # shellcheck disable=SC1091
    source .venv/bin/activate

    python3 -c "
import whisper
print(f'  Carregando modelo: $WHISPER_MODEL')
model = whisper.load_model('$WHISPER_MODEL')
print('  Modelo baixado com sucesso!')
"
    deactivate
    cd "$ROOT_DIR"
    print_ok "Modelo Whisper '$WHISPER_MODEL' pronto"
}

setup_claude_desktop() {
    print_step "Configurando Claude Desktop..."

    CLAUDE_CONFIG_DIR="$HOME/Library/Application Support/Claude"
    CLAUDE_CONFIG="$CLAUDE_CONFIG_DIR/claude_desktop_config.json"
    ROOT_DIR_ESCAPED="${ROOT_DIR//\//\\/}"

    mkdir -p "$CLAUDE_CONFIG_DIR"

    # Caminho para o Python do venv
    PYTHON_PATH="$ROOT_DIR/mcp-server/.venv/bin/python"
    SERVER_PATH="$ROOT_DIR/mcp-server/server.py"

    if [[ -f "$CLAUDE_CONFIG" ]]; then
        print_info "Claude Desktop já tem configuração. Fazendo backup..."
        cp "$CLAUDE_CONFIG" "${CLAUDE_CONFIG}.backup.$(date +%Y%m%d_%H%M%S)"
    fi

    # Verifica se já tem a entrada whatsapp
    if [[ -f "$CLAUDE_CONFIG" ]] && python3 -c "
import json, sys
with open('$CLAUDE_CONFIG') as f:
    config = json.load(f)
sys.exit(0 if 'whatsapp' in config.get('mcpServers', {}) else 1)
" 2>/dev/null; then
        print_info "Configuração WhatsApp já existe no Claude Desktop"
    else
        # Cria ou atualiza configuração
        python3 << PYEOF
import json
import os

config_path = "$CLAUDE_CONFIG"
python_path = "$PYTHON_PATH"
server_path = "$SERVER_PATH"
env_path = "$ROOT_DIR/.env"

# Carrega config existente ou cria nova
if os.path.exists(config_path):
    with open(config_path, 'r') as f:
        try:
            config = json.load(f)
        except json.JSONDecodeError:
            config = {}
else:
    config = {}

if "mcpServers" not in config:
    config["mcpServers"] = {}

config["mcpServers"]["whatsapp"] = {
    "command": python_path,
    "args": [server_path],
    "env": {
        "MESSAGES_DB": "$ROOT_DIR/data/messages.db",
        "MEDIA_DIR": "$ROOT_DIR/data/media",
        "WHISPER_MODEL": "${WHISPER_MODEL:-small}",
        "WHISPER_LANGUAGE": "${WHISPER_LANGUAGE:-pt}"
    }
}

with open(config_path, 'w') as f:
    json.dump(config, f, indent=2, ensure_ascii=False)

print("  Configuração salva em: " + config_path)
PYEOF
        print_ok "Claude Desktop configurado"
    fi

    # Também cria o arquivo de exemplo
    cat > "$ROOT_DIR/claude-config/claude_desktop_config.json.example" << JSONEOF
{
  "mcpServers": {
    "whatsapp": {
      "command": "$ROOT_DIR/mcp-server/.venv/bin/python",
      "args": ["$ROOT_DIR/mcp-server/server.py"],
      "env": {
        "MESSAGES_DB": "$ROOT_DIR/data/messages.db",
        "MEDIA_DIR": "$ROOT_DIR/data/media",
        "WHISPER_MODEL": "small",
        "WHISPER_LANGUAGE": "pt"
      }
    }
  }
}
JSONEOF
}

make_scripts_executable() {
    print_step "Tornando scripts executáveis..."
    chmod +x "$ROOT_DIR/install.sh"
    chmod +x "$ROOT_DIR/start.sh"
    chmod +x "$ROOT_DIR/stop.sh"
    print_ok "Scripts com permissão de execução"
}

print_success() {
    echo ""
    echo -e "${GREEN}============================================${NC}"
    echo -e "${GREEN}  Instalação concluída com sucesso!         ${NC}"
    echo -e "${GREEN}============================================${NC}"
    echo ""
    echo "  Próximos passos:"
    echo ""
    echo "  1. Inicie o sistema:"
    echo -e "     ${BLUE}./start.sh${NC}"
    echo ""
    echo "  2. Escaneie o QR Code com seu WhatsApp:"
    echo "     WhatsApp > Aparelhos Conectados > Conectar aparelho"
    echo ""
    echo "  3. Reinicie o Claude Desktop e use as ferramentas:"
    echo "     - 'Liste minhas conversas do WhatsApp'"
    echo "     - 'Mostre as últimas mensagens do grupo X'"
    echo "     - 'Faça um resumo das mensagens das últimas 24h'"
    echo ""
    echo -e "  ${YELLOW}Dica:${NC} Edite .env para personalizar configurações"
    echo ""
}

# ─────────────────────────────────────────────────────────────────────────────
# Execução principal
# ─────────────────────────────────────────────────────────────────────────────

print_header
check_macos
check_xcode_tools
check_homebrew
install_go
install_python
install_ffmpeg
setup_env
setup_directories

mkdir -p "$ROOT_DIR/claude-config"

build_bridge
setup_python_env
install_whisper_model
setup_claude_desktop
make_scripts_executable

print_success
