# Changelog

All notable changes are documented here, following Keep a Changelog and Semantic Versioning.

## [Unreleased]

### Added

- OpenAI-compatible multipart transcription with JSON, text, and SSE delta/done responses using go-parakeet v0.1.0 and FP32 Parakeet TDT 0.6B v3.
- Bounded upload admission, disk-backed waiting requests, FFmpeg decoding and resampling, exclusive model replicas, and cooperative cancellation.
- Model listing, optional bearer authentication, liveness/readiness endpoints, Prometheus metrics, and structured logs.
- `serve`, `transcribe`, `pull`, `config`, `doctor`, and `bench` commands with strict layered YAML/environment/flag configuration.
- Checksum-pinned model provisioning and native provider-placement diagnostics.
- CPU/CUDA native tests, official OpenAI SDK smoke tests, and Docker recipes for CPU amd64/arm64, desktop CUDA, Jetson Orin, and Jetson Xavier.
- CI checks, versioned binary release workflow, checksums, and dependency license collection.

### Changed

- Raised the initial Go baseline from 1.26.4 to 1.26.9 to include standard-library fixes identified during vulnerability checks.

### Fixed

- Made missing/unknown STT model errors actionable by naming the configured model, and added validation error details to transcription logs.
- Documented Open WebUI's separate STT model setting and multipart voice-mode configuration after diagnosing a model-name mismatch.

[Unreleased]: https://github.com/androiddrew/parakeet-run/commits/main
