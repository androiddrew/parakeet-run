#!/usr/bin/env python3
"""Exercise the official OpenAI SDK against an existing service or a local binary.

Run using the pinned SDK in scripts/sdk-requirements.txt. No OpenAI credentials
or external API calls are used; --base-url must point at the service under test.
"""
import argparse
import gzip
import hashlib
import os
from pathlib import Path
import socket
import struct
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

import openai


EXPECTED = "The little bird landed on the windowsill. Please open the blue door."
ROOT = Path(__file__).resolve().parents[1]


def fixture():
    data = gzip.decompress((ROOT / "testdata/speech.f32.gz").read_bytes())
    assert hashlib.sha256(data).hexdigest() == "f2c2589f09a7535fed93974a6f6ced3f6b4a101567e872f72edf3c2d7e311cbb"
    return struct.pack("<4sI4s4sIHHIIHH4sI", b"RIFF", 36 + len(data), b"WAVE", b"fmt ", 16,
                       3, 1, 16000, 64000, 4, 32, b"data", len(data)) + data


def smoke(base_url, key):
    with openai.OpenAI(base_url=base_url.rstrip("/") + "/v1", api_key=key, max_retries=0, timeout=120) as client:
        model = "parakeet-tdt-0.6b-v3"
        assert model in [m.id for m in client.models.list().data]
        audio = fixture()
        for fmt in ("json", "text"):
            result = client.audio.transcriptions.create(model=model, file=("speech.wav", audio, "audio/wav"), response_format=fmt)
            assert (result.text if fmt == "json" else result) == EXPECTED, result
        deltas, done = [], []
        with client.audio.transcriptions.create(model=model, file=("speech.wav", audio, "audio/wav"), stream=True) as stream:
            for event in stream:
                if event.type == "transcript.text.delta":
                    deltas.append(event.delta)
                elif event.type == "transcript.text.done":
                    done.append(event.text)
                else:
                    raise AssertionError(event)
        assert "".join(deltas) == EXPECTED and done == [EXPECTED], (deltas, done)
        try:
            client.audio.transcriptions.create(model=model, file=("speech.wav", audio), prompt="unsupported")
        except openai.BadRequestError as exc:
            assert exc.param == "prompt", exc
        else:
            raise AssertionError("unsupported prompt accepted")
    print(f"OpenAI SDK {openai.__version__}: models, JSON, text, SSE, and errors passed")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", help="Existing HTTP service, without /v1")
    parser.add_argument("--binary", default="bin/parakeet-run")
    parser.add_argument("--assets", default="assets")
    parser.add_argument("--ort", default="libonnxruntime.so")
    parser.add_argument("--provider", choices=("cpu", "cuda"), default="cpu")
    parser.add_argument("--work-dir", default="tmp")
    parser.add_argument("--key", default="sdk-smoke-test-key")
    args = parser.parse_args()
    if args.base_url:
        smoke(args.base_url, args.key)
        return
    Path(args.work_dir).mkdir(parents=True, exist_ok=True)
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    base = f"http://127.0.0.1:{port}"
    env = dict(os.environ, PARAKEET_RUN_API_KEY=args.key)
    with tempfile.TemporaryDirectory(prefix="sdk-smoke-", dir=args.work_dir) as tmp:
        log_path = Path(tmp) / "server.log"
        with log_path.open("wb") as log:
            process = subprocess.Popen([str(Path(args.binary).resolve()), "serve", "--listen", f"127.0.0.1:{port}",
                                        "--model-dir", args.assets, "--ort-library", args.ort, "--provider", args.provider,
                                        "--temp-dir", tmp], stdout=log, stderr=log, env=env)
            try:
                for _ in range(600):
                    if process.poll() is not None:
                        raise RuntimeError(log_path.read_text())
                    try:
                        with urllib.request.urlopen(base + "/readyz", timeout=1) as response:
                            if response.status == 200:
                                break
                    except (urllib.error.URLError, TimeoutError):
                        pass
                    time.sleep(0.2)
                else:
                    raise TimeoutError("service did not become ready")
                smoke(base, args.key)
            except BaseException:
                print(log_path.read_text())
                raise
            finally:
                if process.poll() is None:
                    process.terminate()
                try:
                    process.wait(timeout=30)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
                    raise
            assert process.returncode == 0, log_path.read_text()
            assert list(Path(tmp).iterdir()) == [log_path], "temporary upload leak"


if __name__ == "__main__":
    main()
