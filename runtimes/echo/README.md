# Echo conformance oracle

The echo image is a deterministic, keyless oracle for the control-plane
contract. Task mode writes logs, emits a fixed usage/result summary, and exits.
Service mode remains alive and emits heartbeats so recast behavior can be
tested without a provider.

During the v1alpha1 transition it intentionally writes the accepted legacy
`{result,tokensUsed,dollarsUsed}` termination payload. It is not the maintained
model-backed runtime; use `runtimes/reference` for the versioned envelope,
provider transports, events, and tools.
