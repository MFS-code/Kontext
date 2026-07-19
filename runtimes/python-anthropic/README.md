# Unmaintained Python Anthropic example

This source is retained only as a migration reference for the original Python
runtime. It is not maintained, built by CI, or published with Kontext
releases. New deployments and provider work should use the Go
[`runtimes/reference`](../reference) image.

The implementation predates the current runtime contract and intentionally
preserves incompatible historical behavior:

- it aliases `KONTEXT_MODEL` values instead of treating them as opaque;
- it invents and enforces a five-minute wallclock default inside the runtime;
- it writes only the legacy termination payload rather than the versioned
  result envelope.

Do not use this image as evidence of conformance with [`SPEC.md`](../../SPEC.md).
The v1alpha1 controller still accepts its legacy result payload solely for
backward compatibility.

For historical investigation, build it explicitly:

```bash
docker build -t kontext-runtime-anthropic:dev runtimes/python-anthropic
```
