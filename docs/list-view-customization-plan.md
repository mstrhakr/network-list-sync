# List View + Customizable Columns Implementation Plan

This plan describes what it would take to add a job list/table view with customizable columns to Network List Sync.

## Current State Summary

The current jobs UI is card-based and rendered in a single path.

- Jobs container and empty state: [ui/index.html](ui/index.html#L746)
- Card rendering logic: [ui/index.html](ui/index.html#L1660)
- Jobs polling/data loading: [ui/index.html](ui/index.html#L1439)
- Existing reusable table styling used by modals: [ui/index.html](ui/index.html#L443)

This gives a good baseline for adding a second jobs presentation mode (cards + table) without changing backend APIs.

## Recommended Approach

Build a lightweight, local table customizer module inspired by your PrintMaster implementation rather than copying everything.

Why:

- Faster than building all advanced behavior from scratch.
- Lower risk than importing the full PrintMaster table feature surface immediately.
- Lets us ship a useful v1 quickly, then layer in advanced controls.

## Feature Levels

### Level 1 (MVP)

- Cards/Table toggle on the main jobs screen
- Table columns with show/hide chooser
- Persist visible columns and order in localStorage
- Basic per-column sorting (single column)
- Row actions matching card actions (run, edit, view, logs, delete)

### Level 2 (Enhancements)

- Drag-and-drop column reorder in picker
- Column width resize
- Pin left/right columns
- CSV export of visible columns
- Quick text filter across visible columns

### Level 3 (Advanced)

- Multi-column sort (shift-click)
- Per-column filter controls
- Saved table presets (compact, ops, debugging)
- Optional server-side persistence for table preferences

## Implementation Breakdown

### 1) Introduce View Mode State

Add a new view mode state and toggle controls.

- New state: jobsViewMode = cards | table
- Persist key: nls_jobs_view_mode
- Render path split:
  - renderJobCards(...)
  - renderJobTable(...)

Estimated: 2-4 hours

### 2) Add Table Markup + Toolbar

Add a dedicated table wrapper and toolbar near the current job list container.

Toolbar controls:

- Columns button (opens picker)
- Reset button
- Optional export button (if enabled)

Estimated: 3-5 hours

### 3) Add Column Definition Model

Create a small schema for job columns:

```js
const JOB_COLUMN_DEFS = [
  { id: 'name', label: 'Name', visible: true, hideable: false, sortKey: 'name' },
  { id: 'primary_endpoint', label: 'Primary Endpoint', visible: true, hideable: true, sortKey: 'controller_name' },
  { id: 'primary_list', label: 'Primary List', visible: true, hideable: true, sortKey: 'network_list_id' },
  { id: 'endpoints', label: 'Endpoints', visible: true, hideable: true, sortKey: 'target_count' },
  { id: 'schedule', label: 'Schedule', visible: true, hideable: true, sortKey: 'schedule' },
  { id: 'retention', label: 'IP Retention', visible: true, hideable: true, sortKey: 'observed_ip_ttl_hours' },
  { id: 'last_run', label: 'Last Run', visible: true, hideable: true, sortKey: 'last_run_at' },
  { id: 'result', label: 'Result', visible: true, hideable: true, sortKey: 'last_result' },
  { id: 'actions', label: 'Actions', visible: true, hideable: false }
];
```

Estimated: 2-3 hours

### 4) Build Column Picker + Persistence

- Checkbox toggle for visible columns
- Save/load config from localStorage
- Reset to defaults

Storage key proposal:

- nls_table_config_jobs

Estimated: 5-8 hours

### 5) Sorting + Row Rendering

- Clickable sortable headers
- ASC/DESC toggle
- Stable compare logic with null handling

Estimated: 4-6 hours

### 6) Wire Existing Actions Into Table Rows

Reuse existing job action handlers to avoid behavior drift.

- runJob(id)
- editJob(id)
- showNetworkList(id)
- showLogs(id)
- deleteJob(id)

Estimated: 1-2 hours

### 7) Mobile and Responsive Rules

- Keep cards as default on narrow screens
- Optionally hide table toggle below a breakpoint
- If table shown on mobile, use horizontal scroll container

Estimated: 2-4 hours

### 8) Testing Scope

Manual validation:

- View mode persistence after refresh
- Column visibility and reset
- Sort behavior and null cases
- All row actions still work

Automated tests (later):

- UI E2E for toggle + column picker + persistence

Estimated: 4-8 hours initially

## Estimated Total Effort

- MVP (Level 1): about 2-4 days
- Level 1 + Level 2: about 5-8 days
- Full Level 1-3: about 2-3 weeks

These estimates include implementation, UX polish, and basic validation.

## Reuse Strategy From PrintMaster

Practical strategy:

1. Reuse architecture ideas (column defs, toolbar, picker, localStorage config) directly.
2. Start with a trimmed customizer module focused only on jobs.
3. Avoid importing advanced capabilities on day one (pinning, multi-sort, resize).
4. Add advanced features incrementally only if they prove valuable in your workflow.

This gives most of the value quickly while keeping code size and complexity manageable.

## Suggested Milestones

1. Milestone A: Cards/Table toggle + static table + action parity
2. Milestone B: Column picker + persistence + reset
3. Milestone C: Sort + responsive polish
4. Milestone D: Optional advanced customizer features

## Risks And Mitigations

- Risk: UI complexity grows in a single HTML file.
  Mitigation: move table customizer into a separate script section/file once MVP works.

- Risk: behavior drift between cards and table actions.
  Mitigation: share action handlers and format helpers.

- Risk: mobile usability regressions.
  Mitigation: keep cards as mobile-first default and gate table UI by viewport.
