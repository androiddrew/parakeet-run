# Speech fixture

`speech.f32.gz` is the unmodified `clean-us` input from
[go-parakeet v0.1.0](https://github.com/androiddrew/go-parakeet/tree/9df9358149d4fb86c1759aeb8fb6942393e9adae/testdata/native).
It contains 60,971 little-endian float32 samples at 16 kHz, mono.

Source blob: `f2c2589f09a7535fed93974a6f6ced3f6b4a101567e872f72edf3c2d7e311cbb.f32.gz`.
The filename's SHA-256 identifies the uncompressed samples. Tests verify it.

Expected FP32 transcript:

> The little bird landed on the windowsill. Please open the blue door.

Copyright 2026 Drew Bednar. Apache-2.0, as declared by the upstream
`testdata/native/PROVENANCE.json`. The original prompt was synthesized using
Kokoro v1.0, voice `af_heart`, speed 1; the upstream fixture was resampled to
16 kHz before conversion to float32. No model weights are included here.

This fixture verifies integration and repeatability, not general ASR accuracy.
