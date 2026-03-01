#!/usr/bin/env python3
"""
WhatsApp MCP Server
Expõe ferramentas para o Claude interagir com mensagens do WhatsApp.
"""

import os
import sys
from pathlib import Path
from datetime import datetime, timedelta
from typing import Optional
from dotenv import load_dotenv

# Carrega variáveis de ambiente do .env (busca na raiz do projeto)
_root = Path(__file__).parent.parent
load_dotenv(_root / ".env")

# Adiciona o diretório do servidor ao path
sys.path.insert(0, str(Path(__file__).parent))

import json
import urllib.request
import urllib.parse

import database as db
import transcriber

from mcp.server.fastmcp import FastMCP

BRIDGE_PORT = os.getenv("BRIDGE_PORT", "8765")


def _download_audio_from_bridge(msg_id: str, chat_jid: str) -> Optional[str]:
    """Pede ao bridge Go para baixar um áudio histórico. Retorna o caminho ou None."""
    params = urllib.parse.urlencode({"id": msg_id, "jid": chat_jid})
    url = f"http://localhost:{BRIDGE_PORT}/download?{params}"
    try:
        with urllib.request.urlopen(url, timeout=60) as resp:
            data = json.loads(resp.read())
            return data.get("path")
    except Exception as e:
        return None

mcp = FastMCP(
    "WhatsApp",
    instructions="""Você tem acesso às mensagens do WhatsApp do usuário.
Use as ferramentas disponíveis para:
- Listar conversas e grupos
- Ler mensagens de conversas específicas
- Buscar mensagens por texto
- Transcrever mensagens de áudio
- Gerar resumos de conversas

Sempre responda em português, a menos que o usuário peça outro idioma.
Ao mostrar mensagens, indique claramente quem enviou e o horário.
Para grupos, indique o nome do participante em cada mensagem.""",
)


# ─────────────────────────────────────────────────────────────────────────────
# Ferramentas de Conversas
# ─────────────────────────────────────────────────────────────────────────────


@mcp.tool()
def listar_conversas(
    limite: int = 20,
    apenas_grupos: bool = False,
    apenas_contatos: bool = False,
) -> str:
    """
    Lista as conversas do WhatsApp ordenadas pela mais recente.

    Args:
        limite: Número máximo de conversas a retornar (padrão: 20)
        apenas_grupos: Se True, mostra apenas grupos
        apenas_contatos: Se True, mostra apenas contatos individuais
    """
    chats = db.get_chats(
        limit=limite,
        only_groups=apenas_grupos,
        only_contacts=apenas_contatos,
    )

    if not chats:
        return "Nenhuma conversa encontrada. O WhatsApp Bridge está rodando?"

    tipo_filtro = ""
    if apenas_grupos:
        tipo_filtro = " (apenas grupos)"
    elif apenas_contatos:
        tipo_filtro = " (apenas contatos)"

    lines = [f"## Conversas do WhatsApp{tipo_filtro}\n"]
    lines.append(f"Mostrando {len(chats)} conversa(s):\n")

    for i, chat in enumerate(chats, 1):
        nome = chat["name"] or chat["jid"]
        tipo = "Grupo" if chat["is_group"] else "Contato"
        ultima_msg = db.format_timestamp(chat["last_message_time"])
        preview = chat.get("last_message") or ""
        if preview and len(preview) > 60:
            preview = preview[:60] + "..."

        lines.append(f"**{i}. {nome}** ({tipo})")
        lines.append(f"   JID: `{chat['jid']}`")
        lines.append(f"   Última mensagem: {ultima_msg}")
        if preview:
            lines.append(f"   Preview: {preview}")
        lines.append("")

    return "\n".join(lines)


@mcp.tool()
def listar_grupos() -> str:
    """
    Lista todos os grupos do WhatsApp com informações resumidas.
    """
    return listar_conversas(limite=50, apenas_grupos=True)


# ─────────────────────────────────────────────────────────────────────────────
# Ferramentas de Mensagens
# ─────────────────────────────────────────────────────────────────────────────


@mcp.tool()
def ler_mensagens(
    conversa: str,
    limite: int = 50,
    tipo: Optional[str] = None,
) -> str:
    """
    Lê as mensagens de uma conversa específica.

    Args:
        conversa: Nome do contato/grupo OU JID (ex: 5511999999999@s.whatsapp.net)
        limite: Número máximo de mensagens (padrão: 50)
        tipo: Filtrar por tipo: 'text', 'audio', 'ptt', 'image', 'video', 'document'
    """
    # Tenta resolver o nome para JID
    chat_jid = _resolve_chat(conversa)
    if not chat_jid:
        return (
            f"Conversa '{conversa}' não encontrada.\n"
            f"Use listar_conversas() para ver as conversas disponíveis."
        )

    chat_info = db.get_chat_info(chat_jid)
    types_filter = [tipo] if tipo else None

    messages = db.get_messages(chat_jid, limit=limite, message_types=types_filter)

    if not messages:
        return f"Nenhuma mensagem encontrada em '{conversa}'."

    nome_chat = chat_info["name"] if chat_info else conversa
    tipo_chat = "Grupo" if (chat_info and chat_info["is_group"]) else "Conversa"

    lines = [f"## {tipo_chat}: {nome_chat}\n"]
    lines.append(f"Mostrando {len(messages)} mensagem(ns):\n")
    lines.append("---")

    for msg in messages:
        ts = db.format_timestamp(msg["timestamp"])
        is_from_me = msg["is_from_me"]
        sender = "Você" if is_from_me else (msg["sender_name"] or "Desconhecido")
        msg_type = msg["message_type"]

        # Conteúdo da mensagem
        content = msg["content"] or ""
        if msg["transcription"]:
            content = f"{content}\n   🎤 Transcrição: {msg['transcription']}"
        elif msg_type in ("audio", "ptt") and msg.get("media_path"):
            content = f"{content}\n   💡 Use transcrever_audio('{msg['id']}', '{chat_jid}') para transcrever"

        emoji = _get_type_emoji(msg_type)
        direction = "→" if is_from_me else "←"

        lines.append(f"\n**{direction} {sender}** [{ts}] {emoji}")
        lines.append(f"{content}")

    return "\n".join(lines)


@mcp.tool()
def buscar_mensagens(
    texto: str,
    conversa: Optional[str] = None,
    limite: int = 20,
) -> str:
    """
    Busca mensagens por texto em todas as conversas ou em uma específica.

    Args:
        texto: Texto a buscar nas mensagens
        conversa: Nome ou JID da conversa para limitar a busca (opcional)
        limite: Número máximo de resultados (padrão: 20)
    """
    chat_jid = None
    if conversa:
        chat_jid = _resolve_chat(conversa)
        if not chat_jid:
            return f"Conversa '{conversa}' não encontrada."

    results = db.search_messages(texto, chat_jid, limite)

    if not results:
        scope = f"em '{conversa}'" if conversa else "em todas as conversas"
        return f"Nenhuma mensagem encontrada com '{texto}' {scope}."

    scope_label = f"em '{conversa}'" if conversa else "em todas as conversas"
    lines = [f"## Resultados para '{texto}' {scope_label}\n"]
    lines.append(f"Encontrado(s): {len(results)} mensagem(ns)\n")

    for msg in results:
        ts = db.format_timestamp(msg["timestamp"])
        sender = "Você" if msg["is_from_me"] else (msg["sender_name"] or "?")
        chat_name = msg.get("chat_name") or msg["chat_jid"]
        emoji = _get_type_emoji(msg["message_type"])

        lines.append(f"**[{chat_name}]** {sender} - {ts} {emoji}")
        lines.append(f"> {msg['content']}")
        lines.append(f"  JID: `{msg['chat_jid']}`")
        lines.append("")

    return "\n".join(lines)


# ─────────────────────────────────────────────────────────────────────────────
# Ferramentas de Áudio / Transcrição
# ─────────────────────────────────────────────────────────────────────────────


@mcp.tool()
def transcrever_audio(
    id_mensagem: str,
    jid_conversa: str,
) -> str:
    """
    Transcreve uma mensagem de áudio ou voz (PTT) usando Whisper.

    Args:
        id_mensagem: ID da mensagem de áudio
        jid_conversa: JID da conversa onde está a mensagem
    """
    if not transcriber.is_whisper_available():
        return (
            "Whisper não está instalado.\n"
            "Execute: pip install openai-whisper\n"
            "Depois reinicie o MCP server."
        )

    if not transcriber.is_ffmpeg_available():
        return (
            "ffmpeg não encontrado.\n"
            "Execute: brew install ffmpeg"
        )

    msg = db.get_message_by_id(id_mensagem, jid_conversa)
    if not msg:
        return f"Mensagem '{id_mensagem}' não encontrada em '{jid_conversa}'."

    if msg["message_type"] not in ("audio", "ptt"):
        return f"A mensagem '{id_mensagem}' não é um áudio (tipo: {msg['message_type']})."

    # Retorna transcrição existente se já foi feita
    if msg.get("transcription"):
        return (
            f"**Transcrição (em cache):**\n{msg['transcription']}\n\n"
            f"*Áudio de {msg['sender_name'] or 'Desconhecido'} "
            f"em {db.format_timestamp(msg['timestamp'])}*"
        )

    media_path = msg.get("media_path")

    # Se não tem arquivo local, tenta baixar sob demanda via bridge API
    if not media_path or not os.path.exists(media_path or ""):
        print(f"[MCP] Solicitando download ao bridge: {id_mensagem}", flush=True)
        media_path = _download_audio_from_bridge(id_mensagem, jid_conversa)
        if not media_path:
            return (
                "Não foi possível baixar o áudio.\n"
                "Possíveis causas:\n"
                "• Mensagem muito antiga (WhatsApp expira mídias após ~60 dias)\n"
                "• Bridge não está rodando — execute `./start.sh`\n"
                "• Metadados de download não disponíveis para esta mensagem"
            )

    if not os.path.exists(media_path):
        return f"Arquivo de áudio não encontrado: {media_path}"

    try:
        print(f"[MCP] Transcrevendo áudio: {media_path}", flush=True)
        texto = transcriber.transcribe_audio_message(media_path)

        # Salva a transcrição no banco
        db.update_transcription(id_mensagem, jid_conversa, texto)

        sender = msg["sender_name"] or "Desconhecido"
        ts = db.format_timestamp(msg["timestamp"])

        return (
            f"**Transcrição:**\n{texto}\n\n"
            f"*Áudio de {sender} em {ts}*"
        )

    except Exception as e:
        return f"Erro ao transcrever áudio: {str(e)}"


@mcp.tool()
def listar_audios(
    conversa: Optional[str] = None,
    limite: int = 10,
) -> str:
    """
    Lista mensagens de áudio disponíveis para transcrição.

    Args:
        conversa: Nome ou JID da conversa (opcional, busca em todas se não informado)
        limite: Número máximo de áudios (padrão: 10)
    """
    chat_jid = None
    if conversa:
        chat_jid = _resolve_chat(conversa)
        if not chat_jid:
            return f"Conversa '{conversa}' não encontrada."

    audios = db.get_audio_messages(chat_jid, limite)

    if not audios:
        return "Nenhuma mensagem de áudio encontrada."

    scope = f"em '{conversa}'" if conversa else "em todas as conversas"
    lines = [f"## Mensagens de Áudio {scope}\n"]

    for audio in audios:
        ts = db.format_timestamp(audio["timestamp"])
        sender = "Você" if audio["is_from_me"] else (audio["sender_name"] or "?")
        has_file = "✅" if audio.get("media_path") and os.path.exists(audio.get("media_path", "")) else "❌"
        has_transcript = "📝" if audio.get("transcription") else "⏳"

        tipo = "🎤 PTT" if audio["message_type"] == "ptt" else "🔊 Áudio"
        lines.append(f"**{tipo}** - {sender} [{ts}]")
        lines.append(f"  ID: `{audio['id']}`")
        lines.append(f"  JID: `{audio['chat_jid']}`")
        lines.append(f"  Arquivo: {has_file} | Transcrição: {has_transcript}")
        if audio.get("transcription"):
            lines.append(f"  Transcrição: {audio['transcription'][:100]}...")
        lines.append("")

    lines.append("\n💡 Para transcrever: `transcrever_audio(id_mensagem, jid_conversa)`")
    return "\n".join(lines)


# ─────────────────────────────────────────────────────────────────────────────
# Ferramentas de Análise / Resumo
# ─────────────────────────────────────────────────────────────────────────────


@mcp.tool()
def resumo_conversa(
    conversa: str,
    horas: int = 24,
    limite: int = 200,
) -> str:
    """
    Prepara o contexto de uma conversa para o Claude gerar um resumo.
    Retorna as mensagens formatadas das últimas N horas para análise.

    Args:
        conversa: Nome do contato/grupo ou JID
        horas: Quantas horas de histórico incluir (padrão: 24)
        limite: Número máximo de mensagens a incluir (padrão: 200)
    """
    chat_jid = _resolve_chat(conversa)
    if not chat_jid:
        return f"Conversa '{conversa}' não encontrada."

    chat_info = db.get_chat_info(chat_jid)

    # Calcula timestamp de corte
    cutoff = int((datetime.now() - timedelta(hours=horas)).timestamp())
    messages = db.get_messages(chat_jid, limit=limite)

    # Filtra mensagens dentro do período
    messages = [m for m in messages if m["timestamp"] >= cutoff]

    if not messages:
        return (
            f"Nenhuma mensagem nas últimas {horas}h em '{conversa}'.\n"
            f"Tente aumentar o parâmetro 'horas'."
        )

    nome_chat = chat_info["name"] if chat_info else conversa
    is_group = chat_info and chat_info["is_group"]

    lines = [
        f"## Contexto para Resumo: {nome_chat}",
        f"Período: últimas {horas}h | {len(messages)} mensagens\n",
        "---",
        "",
    ]

    for msg in messages:
        ts = db.format_timestamp(msg["timestamp"])
        is_from_me = msg["is_from_me"]
        sender = "Você" if is_from_me else (msg["sender_name"] or "?")

        content = msg["content"] or ""
        if msg.get("transcription"):
            content = f"[ÁUDIO TRANSCRITO: {msg['transcription']}]"
        elif msg["message_type"] in ("audio", "ptt"):
            content = "[Mensagem de áudio - não transcrita]"
        elif msg["message_type"] == "image":
            content = f"[Imagem] {content}"
        elif msg["message_type"] in ("video", "document", "sticker"):
            content = content or f"[{msg['message_type'].capitalize()}]"

        lines.append(f"[{ts}] **{sender}**: {content}")

    lines.append("\n---")
    lines.append(
        f"\n*Contexto pronto. Agora analise estas mensagens e forneça "
        f"um resumo detalhado das principais discussões, decisões e "
        f"tópicos abordados em '{nome_chat}'.*"
    )

    return "\n".join(lines)


@mcp.tool()
def estatisticas() -> str:
    """
    Mostra estatísticas gerais do banco de dados WhatsApp.
    """
    stats = db.get_stats()

    lines = [
        "## Estatísticas do WhatsApp Local\n",
        f"- **Total de conversas:** {stats['total_chats']}",
        f"- **Grupos:** {stats['total_groups']}",
        f"- **Contatos individuais:** {stats['total_chats'] - stats['total_groups']}",
        f"- **Total de mensagens:** {stats['total_messages']:,}",
        f"- **Mensagens de áudio:** {stats['total_audio']}",
        f"- **Áudios transcritos:** {stats['transcribed_audio']}",
        f"- **Mensagem mais antiga:** {stats['oldest_message']}",
        f"- **Mensagem mais recente:** {stats['newest_message']}",
        "",
        "### Status dos serviços:",
        f"- Whisper: {'✅ Disponível' if transcriber.is_whisper_available() else '❌ Não instalado'}",
        f"- ffmpeg: {'✅ Disponível' if transcriber.is_ffmpeg_available() else '❌ Não encontrado'}",
    ]

    return "\n".join(lines)


# ─────────────────────────────────────────────────────────────────────────────
# Funções auxiliares (não expostas como tools)
# ─────────────────────────────────────────────────────────────────────────────


def _resolve_chat(identifier: str) -> Optional[str]:
    """
    Resolve um nome ou JID parcial para o JID completo de uma conversa.
    Tenta corresponder por JID exato, depois por nome (case-insensitive).
    """
    with db.get_connection() as conn:
        # Tentativa 1: JID exato
        row = conn.execute(
            "SELECT jid FROM chats WHERE jid = ?", (identifier,)
        ).fetchone()
        if row:
            return row["jid"]

        # Tentativa 2: busca por nome (case-insensitive, parcial)
        row = conn.execute(
            "SELECT jid FROM chats WHERE LOWER(name) LIKE LOWER(?) ORDER BY last_message_time DESC LIMIT 1",
            (f"%{identifier}%",),
        ).fetchone()
        if row:
            return row["jid"]

        # Tentativa 3: JID com sufixo (usuário digitou só o número)
        row = conn.execute(
            "SELECT jid FROM chats WHERE jid LIKE ? ORDER BY last_message_time DESC LIMIT 1",
            (f"%{identifier}%",),
        ).fetchone()
        if row:
            return row["jid"]

    return None


def _get_type_emoji(msg_type: str) -> str:
    """Retorna emoji para o tipo de mensagem."""
    return {
        "text": "💬",
        "audio": "🔊",
        "ptt": "🎤",
        "image": "🖼️",
        "video": "🎬",
        "document": "📄",
        "sticker": "🎭",
        "location": "📍",
        "contact": "👤",
        "reaction": "❤️",
    }.get(msg_type, "📨")


if __name__ == "__main__":
    mcp.run()
