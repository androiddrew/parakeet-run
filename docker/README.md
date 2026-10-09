# Deployment targets

| Target | Platform | Native stack |
|---|---|---|
| CPU | amd64 / arm64 | Debian bookworm; ORT 1.22.0 |
| Desktop CUDA | amd64 | Ubuntu 24.04; CUDA 12.8.1; cuDNN 9; ORT 1.22.0 |
| Jetson Orin | arm64 | JetPack 7.2 / L4T r39.2; CUDA 13.2; cuDNN 9; ORT 1.23.0; sm_87 |
| Jetson Xavier NX/AGX | arm64 | JetPack 5 / L4T r35 host; Ubuntu 22.04 container; CUDA 12.2 + cuda-compat; cuDNN 9.3; ORT 1.22.2; sm_72 |

The Jetson recipes adapt `kokoro-run v0.2.1`. They are supplied for manual
on-device validation and have not been built on this amd64 development host.
The Parakeet FP32 bundle is substantially larger than Kokoro; verify available
RAM, swap, disk space, startup peaks, and steady-state memory on each device.

## Jetson Orin

Run on the target device:

```sh
make image-jetson-orin-onnxruntime
make image-jetson-orin
docker run --rm --runtime nvidia parakeet-run:jetson-orin doctor
docker run --rm --runtime nvidia -p 127.0.0.1:8880:8880 parakeet-run:jetson-orin
```

The runtime build defaults to three compilation jobs and one nvcc thread. To
adjust it, invoke Docker directly with `--build-arg PARALLEL=1` and the same
Dockerfile, context, and tag shown by `make -n image-jetson-orin-onnxruntime`.
BuildKit retains compiled objects in a ccache mount across rebuilds.

## Jetson Xavier

```sh
make image-jetson-xavier-onnxruntime
make image-jetson-xavier
docker run --rm --runtime nvidia parakeet-run:jetson-xavier doctor
docker run --rm --runtime nvidia -p 127.0.0.1:8880:8880 parakeet-run:jetson-xavier
```

The runtime build defaults to two jobs. Its CUDA compatibility directory must
precede the host-injected CUDA 11.4 driver libraries. NVIDIA Tegra redistributables
are verified against their manifests. Attention features requiring sm_80 are
disabled. The application is compiled against Ubuntu 22.04's glibc and joins
the video group (gid 44) for device access.

For both devices, runtime images can be reused from matching `kokoro-run`
builds when the tag/stack matches. `--build-arg ORT_IMAGE=...` selects a different
prepared runtime image. Changing ORT versions also requires updating archive
checksums where applicable and rechecking model/provider compatibility.

## Manual acceptance

For each device record:

1. Device, RAM, JetPack/L4T, image tags, and build outcome.
2. `doctor` output: runtime version, verified model assets, and actual CUDA compute placement.
3. Readiness, model listing, JSON/text/SSE transcription of a known recording.
4. Multiple requests, a recording crossing 30-second windows, and a >300-second recording.
5. Client cancellation followed by a successful request, and SIGTERM shutdown.
6. Cold-start and warm inference timings, host/GPU memory, and any swap use.

The SDK script can target a running container:

```sh
uv run --with-requirements scripts/sdk-requirements.txt python scripts/sdk-smoke.py \
  --base-url http://127.0.0.1:8880
```

Set `--key` when authentication is enabled. These checks require real model
inference; an image build or a CUDA provider registration alone is insufficient.
