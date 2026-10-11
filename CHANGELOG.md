# Changelog

All notable changes to parakeet-run are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/). Each release's section is published
as its GitHub release notes.

## [Unreleased]

## [0.1.0] - 2026-10-11

### Added

- **OpenAI-compatible speech-to-text server**, `parakeet-run serve`, using
  [go-parakeet v0.1.0](https://github.com/androiddrew/go-parakeet) and the FP32
  NVIDIA Parakeet TDT 0.6B v3 model: `POST /v1/audio/transcriptions` with `json`
  and `text` responses and SSE streaming (`stream=true`), `GET /v1/models`,
  optional model aliases, optional bearer-key auth (`PARAKEET_RUN_API_KEY`),
  `/healthz`, `/readyz` and Prometheus `/metrics`. Unsupported OpenAI parameters
  are rejected with a 400 naming the parameter; a wrong or missing model names
  the configured model.
- **Open WebUI** voice mode works as an OpenAI STT engine; the README covers the
  settings, including the separate STT model setting.
- **Bounded capacity**: replicas with a bounded admission queue (429 when full),
  disk-spooled uploads, upload, decode and request timeouts, cooperative
  cancellation and graceful shutdown.
- **Audio decoding with FFmpeg**: WAV, MP3, MP4/M4A, WebM, FLAC and Ogg, converted
  to 16 kHz mono with a configurable maximum duration.
- **Commands** `transcribe`, `bench`, `pull`, `config` and `doctor`. `pull`
  downloads the model with SHA-256 checks; `doctor` verifies where ONNX Runtime
  actually placed the model's nodes on CPU or CUDA.
- **Layered configuration** for every command: built-in defaults, then a YAML file
  (`--config` or `PARAKEET_RUN_CONFIG`), then `PARAKEET_RUN_*` environment
  variables, then flags.
- **Docker images**, with ONNX Runtime, FFmpeg and the model built in:
  - CPU, for linux/amd64 and linux/arm64 (`make image-cpu`).
  - CUDA 12.8 for x86-64 NVIDIA GPUs (`make image-cuda`).
  - Jetson Orin on JetPack 7.2, with ONNX Runtime 1.23 built for CUDA 13.2
    (`make image-jetson-orin`).
  - Jetson Xavier on JetPack 5, with ONNX Runtime 1.22 built for CUDA 12.2 through
    `cuda-compat` (`make image-jetson-xavier`).
- Release archives for linux/amd64 and linux/arm64 with SHA256SUMS. Dependency
  licenses and the Parakeet model card ship with the archives and images.

[Unreleased]: https://github.com/androiddrew/parakeet-run/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/androiddrew/parakeet-run/releases/tag/v0.1.0
