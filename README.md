# WhatsApp Local Claude

Sistema local para ler e analisar mensagens e áudios do WhatsApp pessoal usando Claude via linguagem natural. Tudo roda na sua máquina — seus dados nunca saem do seu Mac.

## Como funciona

```
WhatsApp (seu celular)
        ↓  protocolo WhatsApp Web
  Go Bridge (whatsmeow)
        ↓  salva no SQLite
  MCP Server (Python)
        ↓  protocolo MCP
  Claude Desktop
        ↓
  Você — perguntas em linguagem natural
```

- **Go Bridge**: conecta ao seu WhatsApp via protocolo Web, baixa mensagens e áudios automaticamente
- **MCP Server**: expõe ferramentas ao Claude para consultar o banco de dados local
- **Whisper**: transcreve mensagens de voz localmente (sem enviar para nenhum servidor)
- **Claude Desktop**: interface de linguagem natural para interagir com seus dados

## Requisitos

- macOS 12 (Monterey) ou superior
- [Claude Desktop](https://claude.ai/download) instalado
- Conta WhatsApp ativa com acesso ao celular (para escanear QR Code)
- ~2 GB de espaço livre (para modelos Whisper)

## Instalação

```bash
git clone https://github.com/SEU_USUARIO/data-whatsapp-local-claude
cd data-whatsapp-local-claude
./install.sh
```

O script instala automaticamente:
- Homebrew (se não tiver)
- Go, Python 3, ffmpeg
- Dependências Python (mcp, openai-whisper, etc.)
- Compila o WhatsApp Bridge
- Baixa o modelo Whisper
- Configura o Claude Desktop

## Uso

### 1. Inicie o sistema

```bash
./start.sh
```

Na primeira vez, aparecerá um QR Code nos logs:

```bash
tail -f data/logs/bridge.log
```

Escaneie com: **WhatsApp > Aparelhos Conectados > Conectar aparelho**

### 2. Use o Claude Desktop

Abra o Claude Desktop e faça perguntas em linguagem natural:

| Exemplo de pergunta | Ferramenta usada |
|---|---|
| "Liste minhas conversas" | `listar_conversas` |
| "Mostre as últimas mensagens do João" | `ler_mensagens` |
| "Busque mensagens sobre reunião" | `buscar_mensagens` |
| "Transcreva o áudio da mensagem X" | `transcrever_audio` |
| "Faça um resumo do grupo Família das últimas 24h" | `resumo_conversa` |
| "Liste todos os meus grupos" | `listar_grupos` |
| "Mostre estatísticas do meu WhatsApp" | `estatisticas` |

### 3. Encerre quando terminar

```bash
./stop.sh
```

## Ferramentas MCP disponíveis

| Ferramenta | Descrição |
|---|---|
| `listar_conversas(limite, apenas_grupos, apenas_contatos)` | Lista conversas por recência |
| `listar_grupos()` | Lista todos os grupos |
| `ler_mensagens(conversa, limite, tipo)` | Lê mensagens de uma conversa |
| `buscar_mensagens(texto, conversa, limite)` | Busca por texto (FTS5) |
| `transcrever_audio(id_mensagem, jid_conversa)` | Transcreve áudio com Whisper |
| `listar_audios(conversa, limite)` | Lista áudios disponíveis |
| `resumo_conversa(conversa, horas, limite)` | Prepara contexto para resumo |
| `estatisticas()` | Estatísticas gerais |

## Configuração

Edite o arquivo `.env` para personalizar:

```env
# Modelo Whisper: tiny, base, small (recomendado), medium, large
WHISPER_MODEL=small

# Idioma para transcrição (pt = português)
WHISPER_LANGUAGE=pt

# Baixar áudios automaticamente para transcrição
AUTO_DOWNLOAD_AUDIO=true

# Baixar imagens automaticamente
AUTO_DOWNLOAD_IMAGES=false
```

### Modelos Whisper

| Modelo | Tamanho | Velocidade | Precisão |
|---|---|---|---|
| tiny | ~75 MB | Muito rápido | Baixa |
| base | ~145 MB | Rápido | Média |
| **small** | ~480 MB | **Médio** | **Boa** |
| medium | ~1.5 GB | Lento | Muito boa |
| large | ~3 GB | Muito lento | Excelente |

Para Mac com Apple Silicon (M1/M2/M3), o modelo `medium` roda em boa velocidade.

## Estrutura do projeto

```
data-whatsapp-local-claude/
├── install.sh              # Instalação automática
├── start.sh                # Inicia o bridge
├── stop.sh                 # Para o bridge
├── .env                    # Suas configurações (não commitado)
├── .env.example            # Template de configuração
├── whatsapp-bridge/        # Bridge Go (whatsmeow)
│   ├── main.go
│   └── go.mod
├── mcp-server/             # Servidor MCP Python
│   ├── server.py           # Ferramentas MCP
│   ├── database.py         # Acesso ao SQLite
│   ├── transcriber.py      # Integração Whisper
│   └── requirements.txt
├── claude-config/          # Exemplos de configuração
│   └── claude_desktop_config.json.example
└── data/                   # Dados locais (não commitados)
    ├── messages.db         # Banco SQLite com mensagens
    ├── whatsapp-session.db # Sessão WhatsApp (NÃO compartilhe!)
    ├── media/              # Arquivos de mídia baixados
    │   ├── audio/
    │   ├── ptt/
    │   └── image/
    └── logs/
        └── bridge.log
```

## Banco de dados

O SQLite em `data/messages.db` contém:

- **`chats`**: todas as conversas e grupos
- **`messages`**: mensagens com tipo, conteúdo, transcrição
- **`contacts`**: informações de contatos
- **`messages_fts`**: índice de busca full-text

## Segurança e Privacidade

- **Tudo local**: nenhuma mensagem é enviada para servidores externos
- **Whisper offline**: transcrição acontece 100% na sua máquina
- **Sessão protegida**: `data/whatsapp-session.db` está no `.gitignore` — nunca commite esse arquivo
- **Sem senhas**: a autenticação é via QR Code, igual ao WhatsApp Web

## Solução de problemas

### Bridge não conecta
```bash
tail -f data/logs/bridge.log
```
Se mostrar QR Code, escaneie com o WhatsApp. Se já tem sessão mas não conecta, delete a sessão e refaça o login:
```bash
rm data/whatsapp-session.db
./start.sh
```

### Whisper muito lento
Use um modelo menor no `.env`:
```env
WHISPER_MODEL=base
```

### Claude Desktop não encontra o MCP
1. Verifique se o Claude Desktop foi reiniciado após a instalação
2. Verifique o arquivo de configuração:
```bash
cat ~/Library/Application\ Support/Claude/claude_desktop_config.json
```
3. Certifique-se que os caminhos no arquivo apontam para este projeto

### Erro ao compilar o bridge Go
```bash
cd whatsapp-bridge
go mod tidy
go build .
```

## Atualizações

```bash
git pull
./install.sh
```

O script de instalação é idempotente — pode ser rodado várias vezes com segurança.

## Contribuindo

Pull requests são bem-vindos! Para mudanças grandes, abra uma issue primeiro.

## Licença

MIT — use, modifique e distribua livremente.

---

**Aviso**: Este projeto usa protocolo não-oficial do WhatsApp. Use com responsabilidade e de acordo com os [Termos de Serviço do WhatsApp](https://www.whatsapp.com/legal/terms-of-service).
