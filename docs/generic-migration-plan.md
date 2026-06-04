# Network List Sync Genericization Plan

## Goal
Evolve this repository from a UniFi-focused sync tool into a provider-based network list synchronization platform, while preserving reliability and enabling staged adoption.

## Current Baseline (Verified)
- Repository initialized cleanly as a new git project.
- Both provider client packages are present: UniFi and NPM.
- Runtime wiring currently points to UniFi provider paths and names.
- Full test suite currently passes.

## Working Principles
- Keep behavior stable while introducing abstractions.
- Prefer additive compatibility before destructive renames.
- Keep each phase releasable and testable.
- Avoid big-bang refactors.

## High-Level Architecture Target
- Shared engine handles:
  - DNS resolution
  - scheduling
  - storage
  - run logs
  - diffing and reconciliation flow
- Provider adapters handle:
  - auth and credential validation
  - listing target lists
  - reading target list contents
  - updating target list contents
  - provider-specific merge rules

## Provider Contract (Phase 1 draft)
Define an internal provider interface that supports current flow:

- ValidateConnection(ctx, instance) -> metadata
- ListTargetLists(ctx, instance) -> []TargetListSummary
- GetTargetList(ctx, instance, listID) -> TargetList
- UpdateTargetList(ctx, instance, TargetList) -> error

Define shared models:
- ProviderType: unifi | npm
- ProviderInstance (generic replacement for controller)
- TargetList and TargetItem

## Phased Plan

### Phase 0: Repo Hygiene and Naming Setup
Objective: establish neutral naming and identity without behavior changes.

Tasks:
1. Rename module path to new repository name.
2. Rename app identifiers used in logs/version output.
3. Update README title and scope statement to generic positioning.
4. Keep existing API routes and payload shape unchanged.

Acceptance criteria:
- App builds.
- Existing tests pass.
- No runtime behavior changes in sync path.

### Phase 1: Introduce Provider Abstraction (No UI Breaks)
Objective: isolate provider-specific behavior behind interfaces.

Tasks:
1. Add internal provider package containing interface + shared types.
2. Add UniFi adapter implementing provider interface.
3. Add NPM adapter implementing provider interface.
4. Update sync execution flow to call provider interface only.
5. Keep current store schema and route naming for compatibility.

Acceptance criteria:
- Existing tests pass.
- Add adapter-focused tests for both providers.
- One sync job can run against each provider type.

### Phase 2: Data Model Generalization
Objective: move from controller-centric model to provider instance model.

Tasks:
1. Add provider_type column to instances/controllers table.
2. Add provider_config JSON column for provider-specific fields.
3. Provide migration for existing UniFi rows with defaults.
4. Keep old columns for one transition period.
5. Add store-layer compatibility mapping.

Acceptance criteria:
- Existing databases auto-migrate.
- Existing UniFi jobs continue to run unchanged.
- NPM instances can be represented without field overloading.

### Phase 3: API Compatibility Layer and New Endpoints
Objective: expose generic endpoints while preserving existing clients.

Tasks:
1. Add canonical routes:
   - /api/instances
   - /api/providers
   - /api/instances/{id}/target-lists
2. Keep legacy aliases:
   - /api/controllers/*
   - /api/controllers/{id}/network-lists
3. Introduce explicit provider type in API responses.
4. Mark legacy fields as deprecated in docs.

Acceptance criteria:
- Old UI/API callers continue working.
- New endpoints fully functional for both providers.

### Phase 4: UI Generalization
Objective: make UX provider-aware without breaking existing workflows.

Tasks:
1. Replace Controller wording with Instance in UI text.
2. Add provider selector when creating/editing instance.
3. Render provider-specific auth/config fields dynamically.
4. Keep old field names accepted by backend for compatibility.
5. Improve target list terminology in job forms.

Acceptance criteria:
- Existing UniFi users can still configure/run jobs.
- NPM and UniFi setup both supported in one UI.

### Phase 5: Release, Migration, and Deprecation Policy
Objective: ship safely with clear user guidance.

Tasks:
1. Publish migration guide from UniFi/NPM repos to this generic repo.
2. Tag first generic stable release (v1.0.0 suggested).
3. Keep legacy alias support for 2 minor releases minimum.
4. Add deprecation warnings in API responses/docs.

Acceptance criteria:
- Migration docs validated end-to-end.
- Release notes include compatibility matrix.

## Suggested Branch and PR Strategy
- main: stable integration branch.
- feat/provider-interface
- feat/provider-model-migration
- feat/api-compat-layer
- feat/ui-provider-aware
- docs/migration-guides

Each branch should target one phase and include:
- tests
- docs updates
- release-note fragment

## Priority Backlog (First 10 work items)
1. Rename module path and update imports.
2. Introduce provider interface package.
3. Wrap existing UniFi client in adapter.
4. Wrap existing NPM client in adapter.
5. Refactor syncer to depend on provider interface.
6. Add provider_type to persisted instance records.
7. Add compatibility mapping for old schema fields.
8. Add canonical /api/instances routes.
9. Add provider-aware UI form scaffolding.
10. Write migration guide draft.

## Risk Register
- Risk: schema migration breaks existing DBs.
  - Mitigation: additive migrations only; keep legacy columns until stable.
- Risk: NPM and UniFi list semantics diverge.
  - Mitigation: provider-owned merge strategy; shared model only for common subset.
- Risk: API consumer breakage from naming changes.
  - Mitigation: aliases + deprecation period + explicit docs.
- Risk: large refactor regressions.
  - Mitigation: phase gates with go test and targeted integration checks after each phase.

## Definition of Done for Generic v1
- Both UniFi and NPM are first-class providers.
- New canonical API and UI are provider-aware.
- Legacy compatibility remains functional.
- Migration docs and release notes are complete.
- CI passes with provider test coverage in place.
