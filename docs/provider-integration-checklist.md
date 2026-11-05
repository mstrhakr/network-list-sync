# Provider Integration Checklist

Use this checklist when adding a provider behind `internal/clients/provider.go`. Keep provider-specific behavior inside its adapter; keep sync policy in `internal/syncer/`.

## Contract And Scope

- [ ] Record the product and API versions being targeted, source of the API contract, required permissions, and known limitations.
- [ ] Define how network lists are discovered, identified, read, and updated, including pagination and stable IDs.
- [ ] Confirm whether updates replace the full list or merge entries, and which provider-only fields must survive a round trip.
- [ ] Route construction through the provider factory and UI selection; do not add provider-specific branches to sync orchestration.

## Address Semantics

- [ ] Record accepted address families and entry types: individual IPs, CIDRs/subnets, ranges, and any provider-specific forms.
- [ ] Verify prefix boundaries and canonicalization, including host bits set in CIDR input, `/31`, and `/32`. Do not infer behavior from a UI example alone.
- [ ] Normalize accepted CIDRs to canonical network prefixes before writes; keep single hosts as host entries where the API distinguishes them.
- [ ] Confirm the provider's subnet/range representation preserves the exact address set and cannot broaden a list during conversion.

## Safety And Errors

- [ ] Treat authentication, permission, timeout, malformed response, and partial-result failures explicitly; never log secrets.
- [ ] Confirm source resolution or validation failures stop writes. Empty-output behavior must be explicit and covered by tests.
- [ ] Bound payload size, entry count, and any expansion/fragmentation work before provider writes.
- [ ] Document update atomicity and multi-target partial-failure behavior; do not imply cross-provider transactions.
- [ ] Verify no-op updates, retry behavior, read-after-write behavior, and preservation of unrelated rules or provider-only fields.

## Automated Verification

- [ ] Add adapter tests using `httptest.Server` for request method/path, auth headers, payload shape, response decoding, pagination, and API errors.
- [ ] Test contract edge cases: empty lists, duplicates, unknown fields/enums, numeric or null scalars where relevant, and malformed responses.
- [ ] Test address handling against documented provider semantics, including canonical `/31` and `/32` cases and a subnet hole if CIDR aggregation or exclusions are involved.
- [ ] Run `go vet ./...` and `go test ./...`; add frontend tests when provider selection or UI behavior changes.
- [ ] Document setup, least-privilege permissions, supported versions, and known limitations.

## Live Verification And Beta Status

- [ ] When possible, test against a disposable list on a real endpoint before calling the provider stable. Record endpoint product/version, date, and tested operations; never use a production enforcement list for destructive tests.
- [ ] Verify create/list/read/update/read-back behavior with a host entry and canonical subnet, plus rejection behavior for invalid input. Confirm the provider actually applies the saved rule, not just accepts the API request.
- [ ] If no real endpoint is available, mark the provider **Beta** in user-facing provider documentation and UI. State that only mocks/schema validation are verified; do not imply stable support.
- [ ] Keep the Beta label until a maintainer or independent tester confirms a successful live read/write round trip on a documented provider version. Record the evidence before promoting it to stable.
