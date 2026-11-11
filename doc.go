// Package minicirunner parses and executes containerised CI pipelines defined in a compact YAML format.
//
// It supports job dependency DAGs, deterministic matrix expansion, content-keyed
// immutable caches with hashFiles expressions, artifact extraction, per-job log
// streaming with truncation limits, and pluggable container runtimes (including
// Docker and an in-memory FakeRuntime for testing pipelines without containers).
package minicirunner
