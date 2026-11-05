# Project Guidelines

## Commit Slices
- Commit each completed, validated, independently reviewable slice as soon as it is ready; do not defer all work into one wrap-up commit.
- Use Conventional Commit messages for every commit.
- Stage only files or hunks belonging to the current slice, and review the staged diff before committing.
- Preserve unrelated work. Do not amend or rewrite existing commits unless explicitly requested.
- Push commits only when explicitly requested.

## Project Map
- `main.go` wires SQLite, authentication, the scheduler, embedded UI assets, and the web handler.
- UI markup lives in `ui/templates/`; browser assets and Node tests live in `ui/static/`.
- Keep provider-specific operations behind `internal/clients/provider.go`; sync orchestration belongs in `internal/syncer/`.
- SQLite schema changes belong in `internal/store/` and must work for existing databases through migrations.
- See [docs/development.md](../docs/development.md) for build, test, and release workflows.

## Validation
- For Go changes, run `go vet ./...` and `go test ./...`.
- For frontend changes, run `node --test ui/static/js/*.test.cjs`; tests use Node's built-in runner and need no npm install.
- Web template tests need a complete `auth.Principal` with `Username`, `IsAdmin`, and `AuthProvider`.
