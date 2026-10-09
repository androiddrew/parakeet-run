# Third-party components

The service is Apache-2.0; model and dependency licenses remain separate.

| Component | Source | Terms |
|---|---|---|
| Repository patterns and adapted Jetson recipes | [kokoro-run v0.2.1](https://github.com/androiddrew/kokoro-run/tree/v0.2.1) | Apache-2.0, Drew Bednar |
| Inference engine and speech fixture | [go-parakeet v0.1.0](https://github.com/androiddrew/go-parakeet/tree/v0.1.0) | Apache-2.0, Drew Bednar; embedded vocabulary retains model terms |
| Native runtime ownership | `github.com/androiddrew/ortenv v0.2.0` | MIT, Drew Bednar |
| ONNX Go binding | `github.com/yalue/onnxruntime_go v1.22.0` | MIT |
| Native ONNX Runtime | Microsoft ONNX Runtime | MIT and included ThirdPartyNotices |
| NVIDIA Parakeet TDT 0.6B v3 | [Original model](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3) | [CC-BY-4.0](https://creativecommons.org/licenses/by/4.0/legalcode) |
| ONNX model conversion and embedded vocabulary | [Ilya Stupakov's conversion](https://huggingface.co/istupakov/parakeet-tdt-0.6b-v3-onnx/tree/8f23f0c03c8761650bdb5b40aaf3e40d2c15f1ce) | CC-BY-4.0; weights converted from NVIDIA's model; distributed unmodified |
| `nemo128.onnx` preprocessing graph and inference decision provenance | [onnx-asr](https://github.com/istupakov/onnx-asr) | MIT, Ilya Stupakov; upstream notice is included by the license collector |
| Cobra and pflag | `github.com/spf13/cobra`, `github.com/spf13/pflag` | Apache-2.0 / BSD-3-Clause |
| Prometheus client | `github.com/prometheus/client_golang` | Apache-2.0 |
| YAML | `gopkg.in/yaml.v3` | MIT / Apache-2.0 |
| FFmpeg | Distribution FFmpeg packages | Package-specific LGPL/GPL terms; invoked as a subprocess |
| CUDA/cuDNN | NVIDIA images or redistributables | NVIDIA distribution terms; original notices remain in the images |

The retained [conversion model card](third_party/parakeet-MODEL_CARD.md) is from
the pinned revision. `pull` also installs its checksummed original `README.md`
beside downloaded models. Binary releases retain the card because the linked
Go dependency embeds the conversion vocabulary even when weights are external.

`scripts/collect-licenses.go` gathers license and notice files from every linked
Go module and the Go toolchain. Release archives and images include its output
as `go-dependencies/`. Run `make licenses` to inspect the collected files under
the ignored `tmp/licenses/` directory. Native-runtime images also retain ORT's
license and third-party notices; distribution packages retain their copyright
files in `/usr/share/doc/`. Preserve these notices when redistributing.

See [testdata/README.md](testdata/README.md) for the synthetic fixture's provenance.
