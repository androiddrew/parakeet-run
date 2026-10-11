# parakeet-run

OpenAI-compatible speech-to-text in Go, using
[go-parakeet v0.1.0](https://github.com/androiddrew/go-parakeet/tree/v0.1.0)
and NVIDIA Parakeet TDT 0.6B v3 FP32 ONNX models. A cousin of
[kokoro-run](https://github.com/androiddrew/kokoro-run), sharing its operational
conventions and Linux CPU, CUDA, Orin, and Xavier deployment targets.

## Quick start

Requirements: **Go 1.26.9+**, CGO and a C compiler, FFmpeg, the FP32 model bundle,
and an ONNX Runtime shared library exposing C API 22. Serving needs no Python,
NeMo, or PyTorch. Linux amd64 and arm64 binaries are released; Jetson images are
built on the device.

```sh
make build
bin/parakeet-run pull
bin/parakeet-run doctor --ort-library /opt/onnxruntime/1.22.0-cpu/lib/libonnxruntime.so
bin/parakeet-run serve --ort-library /opt/onnxruntime/1.22.0-cpu/lib/libonnxruntime.so
```

CUDA uses the same binary:

```sh
bin/parakeet-run serve --provider cuda \
  --ort-library /opt/onnxruntime/1.22.0-cuda12/lib/libonnxruntime.so
```

CUDA/cuDNN/provider libraries must be discoverable before startup. See
[ASSETS.md](ASSETS.md) for versions, asset layout, hashes, and runtime ownership.
Use `--listen 127.0.0.1:8881` when another service already occupies port 8880.

## API

```sh
curl http://127.0.0.1:8880/v1/audio/transcriptions \
  -F model=parakeet-tdt-0.6b-v3 -F file=@recording.wav
```

```json
{"text":"The recognized transcript."}
```

| Endpoint | Purpose |
|---|---|
| `POST /v1/audio/transcriptions` | Multipart file transcription |
| `GET /v1/models` | OpenAI model list, including configured aliases |
| `GET /healthz` | Liveness |
| `GET /readyz` | At least one loaded, warmed replica; 503 during background loading or shutdown |
| `GET /metrics` | Prometheus metrics, unless disabled |

Supported form fields are `file`, `model`, `response_format` (`json` or `text`),
and `stream` (`true` or `false`). File and model are required. JSON and
non-streaming are defaults. Duplicates, unknown fields, and unsupported options
return parameter-specific 400 errors. Model aliases are opt-in and still run
Parakeet; they do not change backend capabilities.

FFmpeg decodes WAV, MP3, MP4/M4A (AAC), MPEG/MPGA, WebM (Opus), FLAC, and Ogg
(Vorbis), selecting the first audio stream and converting to mono 16 kHz PCM.
Codec availability follows the installed FFmpeg build. Content must be a media
file, not a URL, local server path, playlist, or raw unlabelled PCM.

### Streaming

```sh
curl -N http://127.0.0.1:8880/v1/audio/transcriptions \
  -F model=parakeet-tdt-0.6b-v3 -F file=@recording.wav -F stream=true
```

Streaming requires omitted/JSON `response_format` and uses `text/event-stream`:

```text
data: {"type":"transcript.text.delta","delta":"The recognized"}

data: {"type":"transcript.text.delta","delta":" transcript."}

data: {"type":"transcript.text.done","text":"The recognized transcript."}
```

The complete file uploads and decodes before inference begins. Deltas are
finalized chunks, not live-microphone or token-by-token output. Preserve leading
spaces when concatenating deltas. Silence may produce only an empty completion.
Only `transcript.text.done` indicates success; no `[DONE]` sentinel is emitted.
An inference failure after headers produces `data: {"error":{...}}` and closes
the stream without a completion event. A disconnected or stalled client may
prevent even that error from being delivered. Proxies should disable buffering
and allow for upload time and a complete inference window before the first event.

### Official OpenAI client

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:8880/v1", api_key="local")
with open("recording.wav", "rb") as audio:
    with client.audio.transcriptions.create(
        model="parakeet-tdt-0.6b-v3", file=audio, stream=True
    ) as events:
        for event in events:
            if event.type == "transcript.text.delta":
                print(event.delta, end="", flush=True)
```

### Open WebUI voice mode

In **Admin Settings → Audio → Speech-to-Text**, configure:

| Setting | Value |
|---|---|
| Engine | OpenAI |
| API base URL | `http://<parakeet-host>:8880/v1`, reachable from the Open WebUI backend |
| STT model | **`parakeet-tdt-0.6b-v3`** |
| Request format, if offered | Multipart |
| API key | Your configured key, or a placeholder when server authentication is disabled |
| Transcription language | Default/automatic; leave unset |

The **STT model setting is separate from the chat model**. A default such as
`whisper-1`, or an empty STT model, will receive HTTP 400 unless you explicitly
configure an appropriate alias. `GET /v1/models` lists accepted model IDs.

Open WebUI's standard voice/call mode detects sound and silence in the browser,
then sends a recorded clip through its backend to `/v1/audio/transcriptions`.
That workflow uses the ordinary JSON transcription response and does not require
server-side VAD, `chunking_strategy`, or SSE. Tested with Open WebUI v0.11.4.
Explicit `language` hints remain unsupported; leaving the language
unset avoids a rejected attempt and client-side fallback.

For HTTP 400 errors, the server's `transcription` warning includes `error`,
`error_code`, and `error_param`. A model mismatch reports the configured canonical
model name. These diagnostics exclude uploaded content and field values.

### Compatibility boundaries

This is a documented subset of OpenAI's transcription API. The model recognizes
25 languages listed in its [model card](third_party/parakeet-MODEL_CARD.md), but
the binding accepts no language hint and returns no detected-language metadata.

Explicitly unsupported parameters include `language`, `languages[]`, `prompt`,
`temperature` (including zero), `keywords[]`, `timestamp_granularities[]`,
`include[]`, speaker-reference fields, and `chunking_strategy`. Unsupported
formats include `verbose_json`, `srt`, `vtt`, and `diarized_json`. There is no
translation, realtime ingestion, or diarization endpoint. Token emission times
are not presented as aligned word timestamps.

Errors use `{"error":{"message":...,"type":...,"param":...,"code":...}}`.
Invalid audio/options return 400, invalid authentication 401, resource limits
413, full admission capacity 429 with `Retry-After: 1`, unavailable models or
request timeouts 503, and unexpected inference failures 500. Slow uploads return
408. Native diagnostics are kept out of API error messages.

## CLI and configuration

```sh
bin/parakeet-run --help
bin/parakeet-run config
bin/parakeet-run transcribe recording.m4a --format json
bin/parakeet-run transcribe recording.wav --format jsonl --output transcript.jsonl
bin/parakeet-run bench recording.wav --runs 3
bin/parakeet-run doctor --provider cuda --log placement.log
```

Output and diagnostic log files are created exclusively. Relative paths are
relative to the working directory. Configuration precedence is:

```text
defaults < --config / PARAKEET_RUN_CONFIG YAML < PARAKEET_RUN_* < explicit flags
```

Unknown YAML fields and multiple documents fail. `config` prints effective YAML
with source comments, without secret values. Environment names follow YAML
paths: `runtime.library` → `PARAKEET_RUN_RUNTIME_LIBRARY`. Durations use Go syntax
(`30s`, `10m`); aliases use comma-separated strings in the environment.

Example `config.yaml`:

```yaml
runtime:
  library: /opt/onnxruntime/1.22.0-cpu/lib/libonnxruntime.so
  provider: cpu
  threads: 1
model:
  dir: assets
  id: parakeet-tdt-0.6b-v3
  aliases: []
  window: 30s
  overlap: 4s
audio:
  ffmpeg: ffmpeg
  max_duration: 30m
  decode_timeout: 2m
server:
  listen: 127.0.0.1:8880
  replicas: 1
  max_queue: 8
  preload: true
  max_upload_bytes: 25000000
  upload_timeout: 1m
  request_timeout: 10m
  write_timeout: 30s
  shutdown_timeout: 2m
  temp_dir: ""
  api_key_file: ""
  metrics: true
logging:
  format: json
  level: info
```

Set `PARAKEET_RUN_API_KEY` or `server.api_key_file` to require a bearer key on
`/v1/*`. An explicitly set environment key takes precedence over the file and
must not be empty. Inline YAML keys are rejected. Health/readiness/metrics are
unauthenticated operational endpoints.

### Capacity and lifecycle

- Admission bounds uploading, queued, and active requests together to
  `replicas + max_queue`. Queued inputs remain on disk; only leased workers decode.
- Uploads are limited to 25,000,000 bytes plus bounded multipart overhead. Decoded
  duration is limited separately: 30 minutes of mono float32 PCM is about 115 MB.
- One engine processes one request at a time. More replicas duplicate substantial
  model/native/GPU memory; measure with `bench` before increasing this setting.
- Native windows default to 30 seconds with four-second overlap. Complete audio
  is still held in PCM during inference, even for SSE output.
- Default preload verifies assets, loads, and warms every replica before binding
  the listener. `preload: false` permits background loading and partial capacity.
  Failed replicas are logged and not automatically retried.
- Cancellation is cooperative between native calls. A deadline cannot interrupt
  an active ONNX call; the engine and buffers remain owned until it returns.
- Shutdown stops admission, wakes queued waiters, joins the model loader, and
  drains work before closing models. Uploads retain their upload deadline. If
  the grace period expires, the CLI exits without destroying busy native sessions.
- ONNX Runtime stays loaded until process exit, including after failed loads.

Metrics have the `parakeet_` prefix and cover request outcomes, stage durations,
audio seconds, readiness, and admission/worker capacity. Logs exclude transcript
contents and original uploaded filenames. `doctor` verifies actual native node
placement in a subprocess; CPU preprocessing is expected with CUDA inference.

## Docker

```sh
make image-cpu
docker run --rm -p 127.0.0.1:8880:8880 parakeet-run:cpu

make image-cuda
docker run --rm --gpus all -p 127.0.0.1:8880:8880 parakeet-run:cuda
```

Images run as UID 10001, contain FFmpeg, and use `/readyz` for healthchecks. CUDA
needs NVIDIA Container Toolkit. CPU supports `PLATFORM=linux/amd64` or
`PLATFORM=linux/arm64`; cross-platform final image steps require binfmt/QEMU or
a native builder. The desktop CUDA image is amd64 only.

For an image without baked models:

```sh
make image-cpu BAKE_MODELS=0
docker volume create parakeet-assets
docker run --rm -v parakeet-assets:/var/lib/parakeet-run/assets parakeet-run:cpu pull
docker run --rm -p 127.0.0.1:8880:8880 \
  -v parakeet-assets:/var/lib/parakeet-run/assets:ro parakeet-run:cpu
```

Host bind mounts must be readable by UID 10001, and writable for `pull`. See
[docker/README.md](docker/README.md) for Jetson builds and manual validation.

## Development and releases

```sh
make test
make lint
PARAKEET_RUN_TEST_ASSETS="$PWD/assets" \
PARAKEET_RUN_TEST_ORT=/opt/onnxruntime/1.22.0-cpu/lib/libonnxruntime.so \
PARAKEET_RUN_TEST_PROVIDER=cpu make test-native
```

Run CUDA separately using provider `cuda` and the CUDA library path. Set
`PARAKEET_RUN_TEST_LONG=1` for the 301-second case. Native tests skip only when
all three required variables are absent; partial configuration fails.

Official SDK smoke test (Python is test-only):

```sh
uv run --with-requirements scripts/sdk-requirements.txt python scripts/sdk-smoke.py \
  --ort /opt/onnxruntime/1.22.0-cpu/lib/libonnxruntime.so
```

CI runs minimum/stable Go, race tests, CPU native/SDK/container tests, formatting,
staticcheck, and govulncheck. CUDA and Jetson builds and inference are validated
by hand on the target hardware.

Track changes in `[Unreleased]` in `CHANGELOG.md`. Before tagging `vX.Y.Z`, create
a dated version section and reset `[Unreleased]`; update comparison links.
`bash scripts/release-notes.sh vX.Y.Z` must succeed. The release workflow builds
Linux amd64/arm64 CGO binaries on bookworm, smoke-tests both, and publishes
archives and `SHA256SUMS`. Native ORT, FFmpeg, and model weights remain external
to binary archives. Docker image publication is not automated.

## License

Service: [Apache-2.0](LICENSE). Model weights and embedded vocabulary retain
CC-BY-4.0. See [NOTICE](NOTICE) and [THIRD_PARTY.md](THIRD_PARTY.md).
