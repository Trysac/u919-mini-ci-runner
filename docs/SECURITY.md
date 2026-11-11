# Security Policy & Threat Model

## Threat Model

`mini-ci-runner` parses YAML pipeline configurations and executes container commands.

### Controls

- **YAML Bomb & Resource Exhaustion Protection**: Input files over 1 MiB are rejected, YAML AST depth above 32 is rejected (`too_deep`), YAML aliases are rejected before decoding, matrix expansion is capped at 256 legs per job and 100 total jobs, workspace copies are capped at 512 MiB, and per-job logs are capped at `MaxLogBytes` (default 1 MiB).
- **Path Traversal Prevention**: All `cache.paths`, `artifacts`, and `hashFiles` globs are validated as clean, relative POSIX paths without `..` segments; invalid paths return an error.
- **No Host Shell Injection**: `NewDockerRuntime` passes arguments directly as an argv list to `exec.CommandContext` and never invokes a host shell.
- **Secret Hygiene**: `OnEvent` notifications never carry environment variable values, and the library performs no global logging.