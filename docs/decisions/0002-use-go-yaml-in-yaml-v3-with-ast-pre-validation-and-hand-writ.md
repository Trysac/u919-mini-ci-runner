# Use go.yaml.in/yaml/v3 with AST pre-validation and hand-written DAG/matrix runner

Status: accepted (generated, pending human review)

## Context

Running CI pipelines locally requires parsing YAML definitions safely while enforcing strict limits against YAML bombs, unknown fields, path traversal, and unbounded concurrency, alongside deterministic testability without requiring a container daemon in unit tests.

## Decision

Use go.yaml.in/yaml/v3 as the sole external dependency to parse YAML into a yaml.Node tree (enforcing depth <= 32 and forbidding aliases) followed by strict decoding with KnownFields(true). Implement matrix expansion, DAG scheduling, content-keyed caching, artifact copying, and the pluggable Runtime interface (DockerRuntime and FakeRuntime) using only the Go standard library.

## Consequences

Pipelines are validated deterministically with machine-readable error codes, unit tests run in milliseconds using FakeRuntime without Docker, and production runs execute cleanly via Docker containers without host shell interpolation.
