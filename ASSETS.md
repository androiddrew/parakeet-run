# Model and native-runtime assets

`parakeet-run pull` provisions the FP32 NVIDIA Parakeet TDT 0.6B v3 ONNX bundle:

- Source: `istupakov/parakeet-tdt-0.6b-v3-onnx` on Hugging Face.
- Revision: `8f23f0c03c8761650bdb5b40aaf3e40d2c15f1ce`.
- Authoritative sizes and SHA-256 values: [internal/assets/manifest.json](internal/assets/manifest.json).

| File | Bytes | Purpose |
|---|---:|---|
| `README.md` | 1,207 | Original conversion model card |
| `nemo128.onnx` | 139,764 | CPU preprocessing |
| `encoder-model.onnx` | 41,770,866 | FP32 encoder graph |
| `encoder-model.onnx.data` | 2,435,420,160 | External encoder weights; must remain beside the graph |
| `decoder_joint-model.onnx` | 72,520,893 | FP32 decoder/joint graph |

The vocabulary is embedded by `go-parakeet`. No INT8 or FP16 variant is served.
Downloads verify existing files, use temporary files beside the destination,
check byte counts and SHA-256, and atomically install completed files. Failed
downloads preserve existing destinations. `serve`, `transcribe`, and `doctor`
verify the full bundle. Serving never downloads missing assets automatically.

```sh
parakeet-run pull --model-dir assets
parakeet-run doctor --model-dir assets --ort-library /path/to/libonnxruntime.so
```

The Go binding is pinned to `onnxruntime_go v1.22.0`, which requires **C API 22**.
Native ONNX Runtime is separately installed: desktop CPU/CUDA use 1.22.0, Orin
uses the 1.23.0 build recipe, and Xavier uses 1.22.2. Newer version strings alone
do not establish model/provider compatibility; run `doctor` and transcription.

Keep the CUDA/shared provider libraries beside their matching runtime. CUDA,
cuDNN, and driver libraries must be discoverable before the process starts.
One runtime library is selected for the entire process lifetime. Changing it
requires a new process. The service closes model sessions but never unloads ORT.

Binary archives contain neither the model weights nor the native runtime.
Images include native runtimes and, by default, verified model weights.
`BAKE_MODELS=0` produces an image requiring a populated asset volume.

Weights and vocabulary retain CC-BY-4.0; the preprocessing graph has MIT
provenance. See [THIRD_PARTY.md](THIRD_PARTY.md).
