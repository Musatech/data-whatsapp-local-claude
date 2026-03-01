"""
Módulo de transcrição de áudio usando OpenAI Whisper (local).
"""

import os
import subprocess
import tempfile
from pathlib import Path
from typing import Optional

WHISPER_MODEL = os.getenv("WHISPER_MODEL", "small")
WHISPER_LANGUAGE = os.getenv("WHISPER_LANGUAGE", "pt")

_whisper_model = None


def _get_model():
    """Carrega o modelo Whisper com lazy loading."""
    global _whisper_model
    if _whisper_model is None:
        try:
            import whisper
            print(f"[Whisper] Carregando modelo '{WHISPER_MODEL}'...", flush=True)
            _whisper_model = whisper.load_model(WHISPER_MODEL)
            print(f"[Whisper] Modelo carregado.", flush=True)
        except ImportError:
            raise RuntimeError(
                "OpenAI Whisper não está instalado. "
                "Execute: pip install openai-whisper"
            )
    return _whisper_model


def _convert_to_wav(input_path: str) -> str:
    """
    Converte arquivo de áudio para WAV usando ffmpeg.
    Retorna o caminho do arquivo WAV temporário.
    """
    tmp = tempfile.NamedTemporaryFile(suffix=".wav", delete=False)
    tmp.close()

    try:
        result = subprocess.run(
            [
                "ffmpeg",
                "-i", input_path,
                "-ar", "16000",   # 16kHz - ideal para Whisper
                "-ac", "1",       # Mono
                "-y",             # Sobrescreve sem perguntar
                tmp.name,
            ],
            capture_output=True,
            text=True,
            timeout=120,
        )
        if result.returncode != 0:
            os.unlink(tmp.name)
            raise RuntimeError(f"ffmpeg falhou: {result.stderr}")
        return tmp.name
    except FileNotFoundError:
        os.unlink(tmp.name)
        raise RuntimeError(
            "ffmpeg não encontrado. Instale com: brew install ffmpeg"
        )
    except subprocess.TimeoutExpired:
        os.unlink(tmp.name)
        raise RuntimeError("Timeout ao converter áudio com ffmpeg")


def transcribe_file(audio_path: str, language: Optional[str] = None) -> dict:
    """
    Transcreve um arquivo de áudio usando Whisper.

    Args:
        audio_path: Caminho para o arquivo de áudio
        language: Idioma do áudio (ex: 'pt', 'en'). None = detecção automática

    Returns:
        dict com 'text' (transcrição) e 'language' (idioma detectado)
    """
    path = Path(audio_path)
    if not path.exists():
        raise FileNotFoundError(f"Arquivo não encontrado: {audio_path}")

    if path.stat().st_size == 0:
        raise ValueError("Arquivo de áudio está vazio")

    lang = language or WHISPER_LANGUAGE or None

    # Converte para WAV se necessário
    wav_path = None
    input_path = str(path)

    suffix = path.suffix.lower()
    if suffix not in (".wav",):
        wav_path = _convert_to_wav(input_path)
        input_path = wav_path

    try:
        model = _get_model()

        options = {
            "fp16": False,  # Usa FP32 para compatibilidade com CPUs sem GPU
            "verbose": False,
        }
        if lang:
            options["language"] = lang

        result = model.transcribe(input_path, **options)
        return {
            "text": result["text"].strip(),
            "language": result.get("language", lang or "desconhecido"),
        }
    finally:
        if wav_path and os.path.exists(wav_path):
            os.unlink(wav_path)


def transcribe_audio_message(media_path: str) -> str:
    """
    Transcreve uma mensagem de áudio do WhatsApp.

    Args:
        media_path: Caminho para o arquivo de áudio

    Returns:
        Texto transcrito
    """
    result = transcribe_file(media_path)
    text = result["text"]

    if not text:
        return "[Áudio sem conteúdo detectado]"

    return text


def is_whisper_available() -> bool:
    """Verifica se o Whisper está disponível."""
    try:
        import whisper  # noqa: F401
        return True
    except ImportError:
        return False


def is_ffmpeg_available() -> bool:
    """Verifica se o ffmpeg está disponível."""
    try:
        result = subprocess.run(
            ["ffmpeg", "-version"],
            capture_output=True,
            timeout=5,
        )
        return result.returncode == 0
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return False
