"""
Módulo de acesso ao banco de dados SQLite do WhatsApp Bridge.
"""

import sqlite3
import os
from datetime import datetime
from typing import Optional
from contextlib import contextmanager

DB_PATH = os.getenv("MESSAGES_DB", "./data/messages.db")


@contextmanager
def get_connection():
    """Context manager para conexão com o banco de dados."""
    conn = sqlite3.connect(DB_PATH)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA journal_mode=WAL")
    conn.execute("PRAGMA foreign_keys=ON")
    try:
        yield conn
    finally:
        conn.close()


def format_timestamp(ts: Optional[int]) -> str:
    """Converte timestamp Unix para string legível."""
    if ts is None:
        return "Desconhecido"
    return datetime.fromtimestamp(ts).strftime("%d/%m/%Y %H:%M")


def get_chats(limit: int = 20, only_groups: bool = False, only_contacts: bool = False) -> list[dict]:
    """
    Retorna lista de conversas ordenadas por última mensagem.

    Args:
        limit: Número máximo de conversas
        only_groups: Se True, retorna apenas grupos
        only_contacts: Se True, retorna apenas contatos individuais
    """
    with get_connection() as conn:
        where_clauses = []
        if only_groups:
            where_clauses.append("is_group = 1")
        elif only_contacts:
            where_clauses.append("is_group = 0")

        where = f"WHERE {' AND '.join(where_clauses)}" if where_clauses else ""

        rows = conn.execute(
            f"""SELECT
                c.jid,
                COALESCE(c.name, c.jid) as name,
                c.is_group,
                c.last_message_time,
                c.unread_count,
                (SELECT content FROM messages m
                 WHERE m.chat_jid = c.jid
                 ORDER BY m.timestamp DESC LIMIT 1) as last_message
            FROM chats c
            {where}
            ORDER BY c.last_message_time DESC
            LIMIT ?""",
            (limit,),
        ).fetchall()

        return [dict(row) for row in rows]


def get_messages(
    chat_jid: str,
    limit: int = 50,
    before_timestamp: Optional[int] = None,
    message_types: Optional[list] = None,
) -> list[dict]:
    """
    Retorna mensagens de uma conversa específica.

    Args:
        chat_jid: JID da conversa (ex: 5511999999999@s.whatsapp.net)
        limit: Número máximo de mensagens
        before_timestamp: Retorna mensagens antes deste timestamp (paginação)
        message_types: Filtra por tipos (text, audio, ptt, image, etc.)
    """
    with get_connection() as conn:
        params = [chat_jid]
        where_clauses = ["chat_jid = ?"]

        if before_timestamp:
            where_clauses.append("timestamp < ?")
            params.append(before_timestamp)

        if message_types:
            placeholders = ",".join("?" * len(message_types))
            where_clauses.append(f"message_type IN ({placeholders})")
            params.extend(message_types)

        where = " AND ".join(where_clauses)
        params.append(limit)

        rows = conn.execute(
            f"""SELECT
                id, chat_jid, sender_jid, sender_name,
                content, message_type, timestamp, is_from_me,
                media_path, transcription
            FROM messages
            WHERE {where}
            ORDER BY timestamp DESC
            LIMIT ?""",
            params,
        ).fetchall()

        return [dict(row) for row in reversed(rows)]


def search_messages(
    query: str,
    chat_jid: Optional[str] = None,
    limit: int = 20,
) -> list[dict]:
    """
    Busca mensagens por texto usando FTS5.

    Args:
        query: Texto a buscar
        chat_jid: Se fornecido, busca apenas nesta conversa
        limit: Número máximo de resultados
    """
    with get_connection() as conn:
        params = [query]
        extra_where = ""

        if chat_jid:
            extra_where = "AND m.chat_jid = ?"
            params.append(chat_jid)

        params.append(limit)

        try:
            rows = conn.execute(
                f"""SELECT
                    m.id, m.chat_jid, m.sender_name,
                    m.content, m.message_type, m.timestamp, m.is_from_me,
                    COALESCE(c.name, m.chat_jid) as chat_name
                FROM messages_fts fts
                JOIN messages m ON m.rowid = fts.rowid
                LEFT JOIN chats c ON c.jid = m.chat_jid
                WHERE messages_fts MATCH ?
                {extra_where}
                ORDER BY m.timestamp DESC
                LIMIT ?""",
                params,
            ).fetchall()
        except sqlite3.OperationalError:
            # Fallback sem FTS se a tabela virtual não existir
            like_query = f"%{query}%"
            params = [like_query]
            extra_where_like = ""
            if chat_jid:
                extra_where_like = "AND m.chat_jid = ?"
                params.append(chat_jid)
            params.append(limit)

            rows = conn.execute(
                f"""SELECT
                    m.id, m.chat_jid, m.sender_name,
                    m.content, m.message_type, m.timestamp, m.is_from_me,
                    COALESCE(c.name, m.chat_jid) as chat_name
                FROM messages m
                LEFT JOIN chats c ON c.jid = m.chat_jid
                WHERE m.content LIKE ?
                {extra_where_like}
                ORDER BY m.timestamp DESC
                LIMIT ?""",
                params,
            ).fetchall()

        return [dict(row) for row in rows]


def get_chat_info(chat_jid: str) -> Optional[dict]:
    """Retorna informações de uma conversa específica."""
    with get_connection() as conn:
        row = conn.execute(
            """SELECT
                jid, COALESCE(name, jid) as name, is_group,
                last_message_time, unread_count,
                (SELECT COUNT(*) FROM messages WHERE chat_jid = chats.jid) as total_messages
            FROM chats
            WHERE jid = ?""",
            (chat_jid,),
        ).fetchone()
        return dict(row) if row else None


def get_message_by_id(message_id: str, chat_jid: str) -> Optional[dict]:
    """Retorna uma mensagem específica pelo ID."""
    with get_connection() as conn:
        row = conn.execute(
            """SELECT id, chat_jid, sender_jid, sender_name,
                content, message_type, timestamp, is_from_me,
                media_path, transcription
            FROM messages
            WHERE id = ? AND chat_jid = ?""",
            (message_id, chat_jid),
        ).fetchone()
        return dict(row) if row else None


def update_transcription(message_id: str, chat_jid: str, transcription: str) -> bool:
    """Atualiza a transcrição de uma mensagem de áudio."""
    with get_connection() as conn:
        result = conn.execute(
            """UPDATE messages
            SET transcription = ?
            WHERE id = ? AND chat_jid = ?""",
            (transcription, message_id, chat_jid),
        )
        conn.commit()
        return result.rowcount > 0


def get_audio_messages(chat_jid: Optional[str] = None, limit: int = 20) -> list[dict]:
    """Retorna mensagens de áudio com ou sem transcrição."""
    with get_connection() as conn:
        params = []
        where_clauses = ["message_type IN ('audio', 'ptt')", "media_path IS NOT NULL"]

        if chat_jid:
            where_clauses.append("chat_jid = ?")
            params.append(chat_jid)

        where = " AND ".join(where_clauses)
        params.append(limit)

        rows = conn.execute(
            f"""SELECT
                id, chat_jid, sender_name, content,
                message_type, timestamp, is_from_me,
                media_path, transcription
            FROM messages
            WHERE {where}
            ORDER BY timestamp DESC
            LIMIT ?""",
            params,
        ).fetchall()

        return [dict(row) for row in rows]


def get_stats() -> dict:
    """Retorna estatísticas gerais do banco de dados."""
    with get_connection() as conn:
        stats = {}

        stats["total_chats"] = conn.execute("SELECT COUNT(*) FROM chats").fetchone()[0]
        stats["total_groups"] = conn.execute(
            "SELECT COUNT(*) FROM chats WHERE is_group = 1"
        ).fetchone()[0]
        stats["total_messages"] = conn.execute("SELECT COUNT(*) FROM messages").fetchone()[0]
        stats["total_audio"] = conn.execute(
            "SELECT COUNT(*) FROM messages WHERE message_type IN ('audio', 'ptt')"
        ).fetchone()[0]
        stats["transcribed_audio"] = conn.execute(
            "SELECT COUNT(*) FROM messages WHERE transcription IS NOT NULL"
        ).fetchone()[0]

        oldest = conn.execute(
            "SELECT MIN(timestamp) FROM messages"
        ).fetchone()[0]
        newest = conn.execute(
            "SELECT MAX(timestamp) FROM messages"
        ).fetchone()[0]

        stats["oldest_message"] = format_timestamp(oldest)
        stats["newest_message"] = format_timestamp(newest)

        return stats
