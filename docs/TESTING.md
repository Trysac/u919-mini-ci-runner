# Testing

## Test Tiers

- **Unit & Acceptance Tests (`minicirunner_test.go`)**: Run locally via `go test -race -cover ./...` using `FakeRuntime` and mock CLI scripts. Covers matrix ordering, DAG scheduling, failure propagation, `hashFiles` SHA-256 calculation, immutable caching, artifact copying, error codes, 20ms timers, cancellation, and log truncation.
- **Conformance Tests (`conformance_test.go`)**: Runs `testdata/example.yml` end-to-end against `NewDockerRuntime("docker")` and `busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662`, skipping cleanly if Docker is not available.