# Potential Additions

This document captures potential feature additions for Network List Sync, ordered by estimated implementation time from shortest to longest.

Estimates are rough and assume familiarity with the current codebase.

## Prioritized By Implementation Time

| Priority | Feature | Estimated Time | Notes |
|---|---|---|---|
| 1 | Job duplicate button | 1-2 hours | Add a UI action and backend path to clone an existing job definition. |
| 2 | Manual "Run All Enabled Jobs" | 2-4 hours | Add one endpoint/UI action to trigger enabled jobs in sequence. |
| 3 | Job list filters/search in UI | 3-5 hours | Filter by status/provider/enabled using existing API payloads. |
| 4 | Run log retention policy | 4-6 hours | Add retention setting and scheduled pruning for old run logs. |
| 5 | Pre-save job validation wizard | 6-10 hours | Validate endpoint access, target list existence, and DNS preview before save. |
| 6 | Per-target retry with backoff | 1-2 days | Retry failing target updates with bounded exponential backoff. |
| 7 | Failure/summary notifications (webhook first) | 1-2 days | Send run outcomes to a configurable webhook with retry handling. |
| 8 | Config import/export (JSON) | 1.5-3 days | Export/import jobs/endpoints safely with redaction and conflict strategy. |
| 9 | Read-only API key auth for app API | 2-4 days | Protect API/UI routes with a simple auth middleware and config. |
| 10 | Prometheus metrics endpoint | 2-4 days | Expose counters/histograms for runs, failures, resolver/provider latency. |
| 11 | Multi-user auth with roles | 1-2 weeks | Add identity, sessions, role-based access checks, and UI permissions. |
| 12 | Provider plugin architecture | 2-4 weeks | Introduce provider interface/plugin model for easier future integrations. |

## Suggested Delivery Phases

### Phase 1: Quick Wins

- Job duplicate button
- Manual "Run All Enabled Jobs"
- Job list filters/search in UI
- Run log retention policy

### Phase 2: Operational Hardening

- Pre-save job validation wizard
- Per-target retry with backoff
- Failure/summary notifications (webhook first)
- Config import/export (JSON)
- Read-only API key auth for app API
- Prometheus metrics endpoint

### Phase 3: Platform Expansion

- Multi-user auth with roles
- Provider plugin architecture

## Assumptions

- Time estimates are implementation-focused and do not include large design changes.
- Integration testing and release coordination may increase total elapsed time.
- Work can be parallelized if contributors split backend/UI tasks.
