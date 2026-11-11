# Project record

## Objective

Run CI pipelines from a YAML file in containers with job dependencies, matrix expansion, content-keyed caches, artifacts and live logs.

## Scope

- In scope: The public API documented in README.md, containerized job execution, YAML parsing with validation, matrix expansion, DAG scheduling, content-keyed caching, and artifact collection.
- Out of scope: Services and sidecar containers, secret management, remote webhook triggers, conditional steps, reusable workflows, Windows containers, and GitHub Actions syntax compatibility.

## Timeline

| Date | Event |
|---|---|
| 2026 | Repository created from the factory utility template. |

## Effort metrics

Measured values only; a value that has not been measured stays "pending measurement".

| Metric | Value |
|---|---|
| Tokens (input / output) | 2453602 / 34983 |
| Hours (human) | pending measurement |
| Cost (USD) | 1.5767 |

## Factory run u919 (measured from API usage)

| Date (UTC) | Phase | Model | Batch | Input | Output | Cache write | Cache read | Cost (USD) |
|---|---|---|---|---|---|---|---|---|
| 2026-11-11 | generation | google/gemini-4-argon | yes | 5796 | 29151 | 0 | 0 | 0.6062 |
| 2026-11-11 | fix-1 | google/gemini-3.8-flash | no | 178773 | 179 | 0 | 23822 | 0.1365 |
| 2026-11-11 | fix-2 | google/gemini-3.8-flash | no | 322022 | 1928 | 0 | 733675 | 0.3038 |
| 2026-11-11 | fix-3 | google/gemini-3.8-flash | no | 88653 | 540 | 0 | 74665 | 0.0741 |
| 2026-11-11 | fix-4 | google/gemini-3.8-flash | no | 102798 | 734 | 0 | 196522 | 0.0946 |
| 2026-11-11 | audit | google/gemini-3.8-flash | no | 444337 | 1073 | 0 | 277399 | 0.3581 |
| 2026-11-11 | docs | google/gemini-3.1-flash-lite | no | 5140 | 1378 | 0 | 0 | 0.0034 |

Total: $1.5767. Prices: vertex line, as measured by its adapter. Providers serve the current version of each model id, so the date tells which version answered; versions, launch stages and price sources per line are in the factory's model register (docs/MODELS.md).
