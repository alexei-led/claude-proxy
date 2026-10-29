# Development

## Requirements

- Go 1.27.3. The module declares Go 1.27.0.
- golangci-lint 2.15.0, with a version-2 configuration.
- Bash for the documented-command tests.
- Node.js 22 or later for optional router-profile validation.

Production code and Go tests use the standard library.
No Makefile target installs or upgrades tools.
Windows is not a target because private-file rules use Unix ownership and no-follow opens.

## Build and tests

| Command          | Purpose                                     |
| ---------------- | ------------------------------------------- |
| `make fmt`       | Format Go files and imports                 |
| `make fmt-check` | Report formatting differences without edits |
| `make lint`      | Run the pinned linter, including tests      |
| `make vet`       | Run `go vet`                                |
| `make test`      | Run unit and contract tests                 |
| `make race`      | Run tests with the race detector            |
| `make build`     | Build `bin/claude-proxy`                    |
| `make check`     | Run all read-only checks and the build      |

Tests use local fake providers, synthetic history and temporary dummy keys.
They do not run a real Claude Code session or classifier.

For repeated integration cases:

```sh
go test ./internal/relay -run 'TestExampleConfigs|TestCrossProviderTurns|TestDocumented' -count=10
```

For the existing router schema validator:

```sh
node scripts/validate-router-profile.mjs /path/to/trusted/claude-model-router examples/proxy-router.json
```

Use only trusted source.
This development tool imports the router's pure `loadConfig` function.
It does not install the router or add a proxy runtime dependency.
Run it after changes to the mixed router profile.
It does not prove live capability support or current prices.

## Change rules

1. Use a non-default branch.
2. Preserve unrelated changes.
3. Add a regression test for each behavior change.
4. Keep production dependencies in the standard library.
5. Keep provider keys and user configuration outside version control.
6. Run the relevant checks before review.
7. State which checks did not run and why.

If a committed base exists, use a sibling worktree for isolated work.
An unborn repository has no such base.
Do not create a commit without authorization to obtain one.

## Git hooks

The hooks require Bash 4+, Gitleaks, Go and the pinned linter.
Changed shell files need ShellCheck and shfmt.
Changed workflows need actionlint and zizmor.

Enable the project hooks explicitly:

```sh
git config --local core.hooksPath scripts/git-hooks
```

Pre-commit uses the staged snapshot for formatting, lint and secret scans.
It also examines changed hook and workflow files.
Pre-push requires a clean checkout at the pushed commit.
It scans Git history and runs `make check`.
The hooks reject default-branch writes and fail if a required tool is absent.
One initial main push is allowed only for a root commit into a completely empty remote.
That bootstrap still requires all push checks. Later updates require PRs.
They do not modify or stage files.

## CI and release

CI runs formatting, lint, vet, unit tests, race tests and a build.
The tag workflow repeats those gates before it builds release assets.
Targets are macOS/Linux on amd64/arm64.
Assets contain binaries, LICENSE and SHA256SUMS.

A first public version must be an experimental prerelease, not a stable release.
The publish command uses `--prerelease --latest=false`.
Release notes come from the committed `docs/releases/<tag>.md` file.
See [readiness](docs/verification.md#release-readiness) for open gates.
Publication requires explicit repository and tag approval.
Release approval does not authorize a live provider call.

## Documentation

| File                                   | Owns                                             |
| -------------------------------------- | ------------------------------------------------ |
| `README.md`                            | Purpose, problem and visual overview             |
| `docs/integration.md`                  | User procedure and rollback                      |
| `docs/configuration.md`                | Schema, authentication, HTTP behavior and limits |
| `docs/design.md`                       | Boundaries and design constraints                |
| `docs/verification.md`                 | Test evidence, gaps and readiness                |
| `docs/plans/implement-claude-proxy.md` | Remaining implementation work                    |

Use short STE sentences in technical documents.
Keep commands, identifiers and quoted output unchanged.
Formal ASD-STE100 compliance needs the official dictionary and a full language review.
A style checker alone does not certify compliance.

The README diagram is hand-authored in `docs/assets/overview.svg`.
It has an accessible title, description and dark-theme rules.
After an edit, render it with `rsvg-convert` and inspect both themes.
Local image validation does not prove GitHub's final page layout.
