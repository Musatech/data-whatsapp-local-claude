# WhatsApp Local Claude

Leia e analise suas mensagens e áudios do WhatsApp via linguagem natural usando o Claude Desktop. Tudo roda na sua máquina — seus dados nunca saem do Mac.

## Como funciona

```
WhatsApp (celular)
      ↓  protocolo WhatsApp Web
Go Bridge (whatsmeow)          ← roda em background, porta 8765
      ↓  salva no SQLite
MCP Server (Python)            ← iniciado automaticamente pelo Claude Desktop
      ↓  protocolo MCP
Claude Desktop (Cowork)
      ↓
Você — perguntas em linguagem natural
```

- **Go Bridge**: conecta ao WhatsApp via protocolo Web, sincroniza mensagens e baixa áudios sob demanda
- **MCP Server**: expõe ferramentas ao Claude para consultar o banco local
- **Whisper**: transcreve mensagens de voz 100% offline
- **SQLite + FTS5**: banco local com busca full-text

## Requisitos

- macOS 12 (Monterey) ou superior
- [Claude Desktop](https://claude.ai/download) instalado
- Conta WhatsApp com acesso ao celular (para escanear QR Code)
- ~2 GB de espaço livre (modelo Whisper)

## Instalação

```bash
git clone https://github.com/Musatech/data-whatsapp-local-claude
cd data-whatsapp-local-claude
./install.sh
```

O script faz tudo automaticamente:

| Passo | O que instala/faz |
|---|---|
| Xcode CLT | Ferramentas de compilação |
| Homebrew | Gerenciador de pacotes |
| Go, Python 3, ffmpeg | Dependências do sistema |
| WhatsApp Bridge | Compila binário Go com suporte FTS5 |
| Python venv | Dependências MCP + Whisper |
| Modelo Whisper | Baixa modelo `small` (~480 MB) |
| Claude Desktop | Configura MCP server com PATH correto |
| LaunchAgent | Diálogo de início automático no login |

> O `install.sh` é **idempotente** — pode ser rodado novamente para atualizar sem perder dados.

## Uso

### 1. Inicie o bridge

```bash
./start.sh
```

Na primeira vez aparece um QR Code nos logs — escaneie com o WhatsApp:

```bash
tail -n +1 -f data/logs/bridge.log
```

**WhatsApp > Aparelhos Conectados > Conectar aparelho**

Após autenticar, o bridge sincroniza o histórico e fica rodando em background.

### 2. Use o Claude Desktop

Reinicie o Claude Desktop após a instalação, abra o Cowork e pergunte em linguagem natural:

```
Liste minhas conversas do WhatsApp
Mostre as últimas mensagens do João
Busque mensagens sobre "reunião de segunda"
Transcreva os áudios do grupo Trabalho
Faça um resumo das últimas 24h do grupo Família
Mostre estatísticas do meu WhatsApp
```

> **O bridge precisa estar rodando** para transcrever áudios. O diálogo do LaunchAgent (ao ligar o Mac) facilita isso.

### 3. Início automático no login

Após a instalação, ao ligar o Mac aparece uma janela perguntando se deseja iniciar o bridge:

- **Sim** → bridge sobe automaticamente, tudo pronto para usar
- **Não** → inicie manualmente com `./start.sh` quando precisar

### 4. Para encerrar

```bash
./stop.sh
```

## Ferramentas MCP disponíveis

| Ferramenta | Parâmetros | Descrição |
|---|---|---|
| `listar_conversas` | `limite`, `apenas_grupos`, `apenas_contatos` | Lista por recência |
| `listar_grupos` | — | Lista todos os grupos |
| `ler_mensagens` | `conversa`, `limite`, `tipo` | Mensagens de uma conversa |
| `buscar_mensagens` | `texto`, `conversa`, `limite` | Busca full-text (FTS5) |
| `transcrever_audio` | `id_mensagem`, `jid_conversa` | Whisper local, baixa se necessário |
| `listar_audios` | `conversa`, `limite` | Áudios disponíveis para transcrição |
| `resumo_conversa` | `conversa`, `horas`, `limite` | Contexto formatado para resumo |
| `estatisticas` | — | Total de mensagens, áudios, status |

### Transcrição de áudio

O fluxo de transcrição funciona assim:

1. Claude chama `transcrever_audio(id, jid)`
2. Se o arquivo já existe localmente → transcreve direto
3. Se não existe → MCP pede ao bridge para baixar via `localhost:8765/download`
4. Bridge reconstrói o áudio a partir dos metadados salvos no history sync e salva em `data/media/ptt/`
5. Whisper transcreve e salva resultado no banco para cache

> Áudios com mais de ~60 dias podem ter URL expirada no WhatsApp e não ser mais baixáveis.

## Configuração

Edite `.env` para personalizar (criado automaticamente pelo `install.sh`):

```env
# Modelo Whisper: tiny, base, small (padrão), medium, large
WHISPER_MODEL=small

# Idioma para transcrição
WHISPER_LANGUAGE=pt

# Download automático de áudios ao receber
AUTO_DOWNLOAD_AUDIO=true

# Download automático de imagens
AUTO_DOWNLOAD_IMAGES=false

# Porta do bridge HTTP
BRIDGE_PORT=8765
```

### Modelos Whisper

| Modelo | Tamanho | Velocidade | Precisão |
|---|---|---|---|
| tiny | ~75 MB | Muito rápido | Baixa |
| base | ~145 MB | Rápido | Média |
| **small** | ~480 MB | **Médio** | **Boa** ← recomendado |
| medium | ~1.5 GB | Lento | Muito boa |
| large | ~3 GB | Muito lento | Excelente |

Apple Silicon (M1/M2/M3/M4) roda `medium` confortavelmente.

## Estrutura do projeto

```
data-whatsapp-local-claude/
├── install.sh                  # Instalação completa (idempotente)
├── start.sh                    # Inicia o bridge em background
├── stop.sh                     # Para o bridge
├── launch-agent-helper.sh      # Script do LaunchAgent (diálogo + start.sh)
├── .env                        # Suas configurações (não commitado)
├── .env.example                # Template
├── whatsapp-bridge/
│   ├── main.go                 # Bridge Go: WhatsApp Web + HTTP API
│   └── go.mod
├── mcp-server/
│   ├── server.py               # Ferramentas MCP (FastMCP)
│   ├── database.py             # Acesso SQLite
│   ├── transcriber.py          # Integração Whisper
│   └── requirements.txt
├── claude-config/
│   └── claude_desktop_config.json.example
└── data/                       # Dados locais (não commitados)
    ├── messages.db             # Banco SQLite com mensagens
    ├── whatsapp-session.db     # Sessão WhatsApp — NUNCA compartilhe!
    ├── media/
    │   ├── ptt/                # Mensagens de voz baixadas
    │   ├── audio/              # Áudios baixados
    │   └── image/              # Imagens (se habilitado)
    └── logs/
        ├── bridge.log
        └── launch-agent.log
```

## Banco de dados

`data/messages.db` (SQLite com FTS5):

| Tabela | Conteúdo |
|---|---|
| `chats` | Conversas e grupos |
| `messages` | Mensagens com tipo, conteúdo, `media_info`, transcrição |
| `contacts` | Informações de contatos |
| `messages_fts` | Índice full-text para busca rápida |

O campo `media_info` armazena metadados (URL, chave de criptografia) para download posterior de áudios históricos.

## Segurança e privacidade

- **100% local**: nenhuma mensagem enviada para servidores externos
- **Whisper offline**: transcrição na sua máquina
- **Sessão protegida**: `whatsapp-session.db` está no `.gitignore` — nunca commite
- **Autenticação por QR Code**: igual ao WhatsApp Web, sem senhas

## Solução de problemas

### Bridge não inicia
```bash
tail -n +1 -f data/logs/bridge.log
```
Se aparecer QR Code, escaneie com o WhatsApp. Se a sessão estiver corrompida:
```bash
./stop.sh
rm data/whatsapp-session.db
./start.sh
```

### Transcrição falha ("bridge não está rodando")
O bridge precisa estar ativo para baixar áudios históricos:
```bash
./start.sh
curl http://localhost:8765/health   # deve retornar {"status":"ok"}
```

### Claude Desktop não reconhece as ferramentas
1. Reinicie o Claude Desktop após a instalação (`Cmd+Q` → reabrir)
2. Verifique a configuração:
```bash
cat ~/Library/Application\ Support/Claude/claude_desktop_config.json
```
3. Rode `./install.sh` novamente para reconfigurar

### Whisper muito lento
Use um modelo menor:
```env
WHISPER_MODEL=base
```
Depois rode `./install.sh` para baixar o novo modelo.

### Erro ao compilar o bridge
```bash
cd whatsapp-bridge
go mod tidy
go build -tags "fts5" -o whatsapp-bridge .
```

### Desativar o diálogo de início automático
```bash
launchctl unload ~/Library/LaunchAgents/com.whatsapp-local-claude.plist
```
Para reativar:
```bash
launchctl load ~/Library/LaunchAgents/com.whatsapp-local-claude.plist
```

## Atualizações

Um único comando faz tudo: baixa as mudanças, recompila o bridge, atualiza dependências Python e reconfigura o Claude Desktop.

```bash
git pull && ./install.sh
```

O que acontece por baixo:
1. `git pull` — baixa a versão mais recente do repositório
2. `./install.sh` — recompila o bridge Go, atualiza pacotes Python, atualiza config do Claude Desktop e recarrega o LaunchAgent

> O `install.sh` é idempotente — não apaga mensagens, sessão ou configurações pessoais (`.env`).

Após atualizar, reinicie o bridge:

```bash
./stop.sh && ./start.sh
```

E reinicie o Claude Desktop (`Cmd+Q` → reabrir) para carregar o novo MCP server.

## Licença

MIT — use, modifique e distribua livremente.

---

**Aviso**: Este projeto usa o protocolo não-oficial do WhatsApp Web. Use com responsabilidade e de acordo com os [Termos de Serviço do WhatsApp](https://www.whatsapp.com/legal/terms-of-service).
