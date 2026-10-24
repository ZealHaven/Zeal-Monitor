# Plan: Open-source sanitization and configurability

Status: complete (2026-10-24)
Scope: spans multiple sessions.

## Goals
1. Remove personal/identifying data from tracked files (PII, paths, IPs, domains, account IDs).
2. Make environment-specific dependencies configurable (tailnet, cloud, system paths, ports).
3. Harden security for a publicly published, local-first tool.

## Phases
- A. Sanitization: anonymize code/tests/docs/ops; move personal plan to example.
- B. Configurability: config file/env for host, ports, paths, domains, cloud features.
- C. Security hardening: API auth, CORS, input validation, audit permissions, XSS.
- D. Local-first mode: disable cloud/tailnet by default; optional system integration.

## Progress
- [x] Rewrote all commit messages to generic subjects.
- [x] Phase A sanitization (core PII removed; tests anonymized).
- [x] Phase B configurability (config example; tailnet domain derived from SelfName; frontend URL env).
- [x] Phase C security (CORS same-origin; expose.sh validation; audit 0600; XSS escaping).
- [x] Phase D local-first (docs/LOCAL_FIRST.md added).

## Progress notes
- Tailnet domain no longer hardcoded; derived from collect.Snapshot.SelfName.
- Cloud/tailnet features remain optional via ops scripts; defaults are loopback/local.

## Notes
- Force-pushed history rewrite is acceptable.
- Keep tests passing after anonymization.
