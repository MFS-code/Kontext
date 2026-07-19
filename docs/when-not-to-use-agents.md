# When not to use an agent

Use an agent when the task genuinely needs model judgment over variable input
and the extra latency, cost, nondeterminism, and operational dependencies are
acceptable. Kubernetes makes those dependencies visible; it does not remove
them.

A deterministic Job, controller, or script is usually better when:

- the input and desired transformation have a complete, testable rule;
- exact repeatability or byte-for-byte output is required;
- a command/API call already performs the task directly;
- retries could duplicate a destructive side effect;
- the task is on a latency-critical path;
- provider network access, quota, or credential handling is unjustified;
- success cannot be checked more reliably than asking another model;
- the workload needs durable workflow state that the runtime does not provide.

Start with a deterministic baseline. Measure correctness, wall time, resource
use, and failure recovery on the same inputs. Add a model only where it
improves a named metric enough to justify provider usage and new failure modes.
Run deterministic graders before any optional model judge.

For mixed workflows, keep parsing, validation, authorization, and irreversible
actions deterministic. Give the agent a bounded proposal step, validate its
structured output, and let ordinary code apply approved changes. A tool being
available is not evidence that it ran; require tool events or downstream
state as proof.

Kontext is also not a workflow engine, retrieval platform, or durable memory
store. Mounted ConfigMap knowledge is static context, not RAG. If the product
needs fresh authorization-aware retrieval, checkpoints, compensation, or
multi-step durable orchestration, use systems designed for those concerns and
run only the model-dependent step as an `AgentRun`.
