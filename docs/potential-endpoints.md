# Potential Endpoints

## Purpose

This document tracks possible provider endpoints for Network List Sync, captures API/capability fit, and helps prioritize implementation effort.

Primary goal:

- Find services where list synchronization is a good technical and product fit.

Secondary goal:

- Deprioritize services that already have strong native dynamic list management, DNS ingestion, or built-in sync workflows.

## Research Scope And Limits

This first pass is populated from known public API patterns and platform behavior as of 2026-06-04.

Environment constraint for this run:

- No live web-request tool is available in this workspace session, so entries are marked with confidence and verification-needed fields.
- A follow-up pass should verify each row with 3-5 focused source checks per service.

Suggested validation budget per service:

1. Official API reference for list object read/update operations.
2. Official auth model and token scopes.
3. Limits/quotas documentation.
4. Product docs for native dynamic list features.
5. One implementation example or SDK docs.

## Capability Fit Criteria

A service is a strong fit if it supports most of the following:

- Durable list object (address group, ip list, alias, set).
- API read of current entries.
- API write/update of entries.
- IP and CIDR item support.
- Stable auth for automation (api key, token, service account).
- Predictable semantics (replace list, patch list, or clear add/remove model).

## Priority Scoring Model

Use a 1-5 score for each factor:

- Technical fit: how closely API semantics match current sync engine model.
- Native overlap risk: higher value means less overlap with built-in product features.
- User value: expected practical demand for this repository audience.
- Implementation effort: higher value means lower effort.
- Operational safety: error handling, rollback options, and API reliability.

Weighted score formula:

- Priority score = 0.35 Technical fit + 0.20 Native overlap risk + 0.25 User value + 0.10 Implementation effort + 0.10 Operational safety

Interpretation:

- 4.0-5.0: Build soon.
- 3.0-3.9: Good candidate after top tier.
- 2.0-2.9: Case-by-case.
- <2.0: Deprioritize.

## Endpoint Candidate Matrix

| Service | Category | Canonical target object | API sync shape | Auth model | Native overlap risk | Technical fit | User value | Implementation effort | Operational safety | Priority score | Confidence | Initial recommendation | Notes |
|---|---|---|---|---|---:|---:|---:|---:|---:|---:|---|---|---|
| OPNsense | Firewall | Alias | read list + update alias | api key + secret | 5 | 5 | 5 | 4 | 4 | 4.75 | medium | Build soon | Strong homelab demand; alias semantics map cleanly to current model. |
| pfSense Plus/CE API | Firewall | Firewall alias | read list + update alias | token/key | 5 | 5 | 5 | 3 | 4 | 4.65 | medium | Build soon | Very aligned object model; verify API differences between CE/Plus modules. |
| Cloudflare | Edge/Zero Trust | IP List | list read + replace/update items | scoped api token | 3 | 4 | 5 | 4 | 4 | 4.05 | medium | Build soon | High demand; partial native overlap because Cloudflare offers dynamic controls in some products. |
| AWS WAFv2 | Cloud security | IPSet | get + update with lock token | iam role/user | 4 | 4 | 4 | 3 | 4 | 3.95 | medium | Strong candidate | Good fit for centralized ingress controls; lock token flow adds complexity. |
| Azure Firewall | Cloud security | IP Group | get + update resource | service principal/managed identity | 4 | 4 | 4 | 2 | 4 | 3.70 | low | Candidate | ARM resource update flow may be heavier than current sync assumptions. |
| GCP VPC | Cloud security | Firewall source ranges | read rule + patch ranges | service account | 3 | 3 | 3 | 2 | 3 | 2.95 | low | Case-by-case | Object model is rule-centric, not list-centric; less ideal abstraction fit. |
| FortiGate | Firewall | Address group | read group + set members | api token | 4 | 4 | 4 | 3 | 3 | 3.75 | medium | Strong candidate | Enterprise demand; verify pagination and workspace/VDOM behavior. |
| Palo Alto NGFW | Firewall | Address object/group | read group + commit update | api key or oauth on newer mgmt | 4 | 4 | 4 | 2 | 2 | 3.40 | low | Candidate after top tier | Commit workflow and config locks increase operational complexity. |
| Cisco Meraki MX | Firewall | L3 firewall allow rules | read rules + replace rule set | api key | 2 | 2 | 4 | 3 | 3 | 2.65 | low | Deprioritize for now | Mostly rule-based model; lacks clean reusable list object in many paths. |
| Nginx Proxy Manager | Proxy | Access list | read list + update items | api key/token | 5 | 5 | 4 | 5 | 4 | 4.70 | high | Already supported | Keep improving quality and tests rather than new adapter work. |
| UniFi Network | Firewall | Traffic matching list | read list + update items | api key | 5 | 5 | 4 | 5 | 4 | 4.70 | high | Already supported | Keep improving quality and tests rather than new adapter work. |
| Tailscale ACL workflows | Identity/network | ACL policy | policy read + policy update | oauth/client credentials | 1 | 2 | 4 | 2 | 3 | 2.20 | low | Deprioritize | Product already has strong native identity-centric policy controls; overlap risk high. |
| Zscaler | SSE | Address group/list | read + update address entities | api credentials/token | 3 | 3 | 3 | 2 | 3 | 2.85 | low | Case-by-case | Could be useful for enterprise edge, but complexity and overlap can be high. |
| CrowdSec bouncer lists | Security ecosystem | decision list inputs | varying plugin APIs | token/api key | 3 | 3 | 3 | 3 | 3 | 3.00 | low | Candidate later | Ecosystem fragmented; value depends on target deployment stack. |

## Top Five Deep Dive

These notes are intentionally narrow: they focus on the top five candidates and what matters for an adapter in this repo.

### 1) OPNsense

- Best fit for the current model among the homelab/self-hosted firewalls.
- Core object shape is a firewall alias/list, which is very close to the sync engine's current replace-the-list workflow.
- Strong advantage: low conceptual mismatch. Items are basically IPs, CIDRs, and name/description metadata.
- What to verify next: API path stability, whether alias CRUD is fully supported by the API vs UI-only flows, and any limits on alias size or category nesting.
- Native overlap risk: low to medium. OPNsense has useful firewall automation features, but a general external feed sync tool still adds value for repeated DNS/IP refresh and multi-source aggregation.
- Adapter implication: likely one of the smallest adapter surfaces to implement and test.

### 2) pfSense

- Also an excellent fit, but likely a little more variable than OPNsense depending on CE vs Plus and which API surface is in use.
- The useful target object is the firewall alias, which maps cleanly to a managed list.
- Best use case: maintain allowlists that change often, especially when administrators want external DNS sources normalized into firewall objects.
- What to verify next: which pfSense release lines expose the needed API operations, whether alias updates are stable enough for idempotent sync, and whether there are any package-specific differences in auth or object naming.
- Native overlap risk: low to medium. pfSense has rich firewall features, but dynamic alias feeds are still a compelling external sync use case if the built-in automation is not already solving the exact problem.
- Adapter implication: prioritize after OPNsense or alongside it if the API surface proves equally stable.

### 3) Cloudflare

- Strong user value because Cloudflare is common in front-door security and access control, but it is not always a perfect list-management match.
- The best target is Cloudflare IP Lists / account-scoped list objects, not general access policies.
- Why it matters: these lists can feed WAF, Access, firewall rules, or other edge enforcement layers, which gives this tool real operational value.
- Native overlap risk: medium to high. Cloudflare already has multiple built-in ways to manage IP-based controls, so this tool is most valuable when users want a reusable external feed pipeline across rulesets or products.
- What to verify next: account/list API semantics, item limits, bulk replace vs incremental patch behavior, and how list items are consumed by downstream rules.
- Adapter implication: good candidate, but best positioned as a provider for shared allowlist objects rather than a deep policy manager.

### 4) AWS WAFv2

- Strong cloud candidate because IP sets are a first-class list object and line up well with the engine's sync pattern.
- The key operational detail is the update lock-token flow: reads and writes are safe, but the adapter needs to handle refresh/retry around concurrent updates.
- Best use case: centrally managed IP sets that feed one or more WAF web ACLs or rules.
- Native overlap risk: medium. AWS absolutely has native security controls, but it does not remove the value of an external source-normalization service that keeps IP feeds current.
- What to verify next: maximum IP set size, lock token lifecycle, IAM permissions for least-privilege automation, and whether regional/global scope changes any behavior.
- Adapter implication: worth doing after the simplest firewall aliases because the sync semantics are good, but the AWS auth/update lifecycle is more involved.

### 5) FortiGate

- Good enterprise target with a clean object model if address groups are exposed consistently.
- A firewall address group is a strong match for a managed list, especially for teams that already rely on Fortinet perimeter controls.
- Important implementation wrinkle: VDOM/workspace/config-commit behavior can change how updates are staged and applied.
- Native overlap risk: medium. FortiGate has many native features, but external DNS/IP feed sync still makes sense for environments with shared security feeds across multiple groups or sites.
- What to verify next: group-member update semantics, auth scope granularity, VDOM support, and any commit/lock requirements that affect sync timing.
- Adapter implication: a solid enterprise provider, but the implementation should be careful around config transaction behavior and tenant scoping.

## Useful Support For This Tool

Usefulness here means practical value specifically for Network List Sync users, not generic market share.

Top usefulness targets:

1. OPNsense
2. pfSense
3. Cloudflare
4. AWS WAFv2
5. FortiGate

Why these rank highest:

- They have list-like primitives compatible with current sync behavior.
- They represent a mix of homelab/self-hosted and production cloud/enterprise use.
- They allow Network List Sync to remain focused on one core job: externalized DNS/IP source normalization into managed list objects.
- They also vary enough in auth and update mechanics to pressure-test the provider abstraction without overfitting to one ecosystem.

Lower usefulness or higher overlap cases:

- Tailscale and some identity-native platforms: often already provide policy-centric workflows that reduce need for this tool.
- Rule-centric firewall APIs without first-class list objects: harder to integrate safely and consistently.

## Service Notes For Native Overlap

Use this quick check before prioritizing implementation:

- Does the service already support automated dynamic list ingestion from URL/DNS natively?
- Does the service already provide time-based refresh of managed feeds?
- Can admins already express the same policy outcome without external sync tooling?

If all answers are yes:

- Set native overlap risk to 1-2.
- Move service to later phase unless there is a compelling interoperability gap.

## Adapter Readiness Checklist

Before implementing a provider adapter, confirm:

- List object can be fetched with stable identifiers.
- Update calls are idempotent or can be made idempotent by client logic.
- API exposes enough error granularity for retry vs fail-fast decisions.
- Rate-limit behavior is known and testable.
- Provider supports least-privilege auth scopes.
- Maximum item counts are documented and handled.

## Proposed Delivery Phases

Phase 1 targets:

- OPNsense
- pfSense

Phase 2 targets:

- Cloudflare
- AWS WAFv2

Phase 3 targets:

- FortiGate
- Azure Firewall IP Groups

Backlog / evaluate later:

- Palo Alto
- Zscaler
- GCP rule-centric adapters
- Identity-native platforms with high overlap

## Verification Backlog

Each row below needs 3-5 targeted checks in a follow-up research pass.

| Service | Verification status | Needed checks |
|---|---|---|
| OPNsense | pending | alias read/write endpoints, auth scopes, alias limits, native dynamic features |
| pfSense | pending | CE vs Plus API differences, alias operations, auth method, limits |
| Cloudflare | pending | list item APIs, token scopes, account-level limits, overlap with native dynamic capabilities |
| AWS WAFv2 | pending | update lock token behavior, ipset limits, iam least-privilege policy |
| Azure Firewall IP Groups | pending | ARM update semantics, throughput limits, role requirements |
| FortiGate | pending | group member update semantics, vdom handling, token permission model |
| Palo Alto | pending | object group update flow, commit model, lock/transaction behavior |
| Cisco Meraki | pending | list-like object support depth, rule replacement safety |
| Zscaler | pending | relevant api family for address lists, auth flow, tenant limits |
| Tailscale | pending | acl api model, native dynamic alternatives, overlap confirmation |

## Suggested Next Actions

1. Validate top 5 services first with capped source checks.
2. Convert this table into a provider feasibility issue template.
3. Start with one homelab target and one cloud target for maximum coverage.
4. Add a provider capability matrix endpoint to the UI later so users can self-select viable adapters.

## Supported Provider Endpoint Examples

These examples are for quick operator guidance and should be verified in each environment.

- UniFi potential endpoints: https://192.168.1.1
- NPM potential endpoints: https://nginx.internal.example.com
