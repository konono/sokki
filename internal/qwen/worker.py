"""Local Qwen3-ASR BF16 worker. Started only by the explicit qwen backend."""
import base64
import contextlib
import io
import json
import sys
import wave
from pathlib import Path

import numpy as np


def emit(value):
    sys.stdout.write(json.dumps(value, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def decode_wav(encoded):
    raw = base64.b64decode(encoded, validate=True)
    with wave.open(io.BytesIO(raw), "rb") as wav:
        if (wav.getnchannels(), wav.getsampwidth(), wav.getframerate()) != (1, 2, 16000):
            raise ValueError("expected 16-kHz mono s16le WAV")
        pcm = wav.readframes(wav.getnframes())
    return np.frombuffer(pcm, dtype="<i2").astype(np.float32) / 32768.0


def generate(model, pcm):
    with contextlib.redirect_stdout(sys.stderr):
        result = model.generate(
            pcm, language="Japanese", max_tokens=512, batch_size=1,
            temperature=0.0, top_p=1.0, top_k=0, stream=False,
            system_prompt=None, hotwords=None, chunk_duration=1200.0,
            min_chunk_duration=1.0, verbose=False)
    tokens = result.generation_tokens
    if tokens is not None and tokens >= 511:
        raise RuntimeError("Qwen generation may have reached the 512-token limit")
    return result.text, tokens


def main():
    model_dir = Path(sys.argv[1])
    if not model_dir.is_absolute() or not model_dir.is_dir() or not (model_dir / "config.json").is_file():
        raise ValueError("Qwen model must be a complete local directory")
    from mlx_audio.stt import load
    with contextlib.redirect_stdout(sys.stderr):
        model = load(str(model_dir))
    generate(model, np.zeros(1600, dtype=np.float32))
    emit({"kind": "ready"})
    expected = 1
    for line in sys.stdin:
        request = None
        try:
            request = json.loads(line)
            sequence = request["id"]
            if sequence != expected:
                raise ValueError("request sequence mismatch")
            pcm = decode_wav(request["wav_base64"])
            text, tokens = generate(model, pcm)
            emit({"id": sequence, "text": text, "generation_tokens": tokens})
            expected += 1
        except Exception as exc:
            emit({"id": request.get("id") if isinstance(request, dict) else None,
                  "error": str(exc)})
            expected += 1


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        emit({"kind": "startup_error", "error": str(exc)})
        raise
