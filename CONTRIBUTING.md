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

PR and main-branch CI run on Linux and macOS.
Both CI and release use `make check` as the shared quality gate.
The lint action installs the pinned binary without a separate lint run.
`make check` performs schema validation, lint, formatting, vet, tests, race tests,
build and the standard-library module check.

The release workflow starts only from a version tag.
It first requires nonempty committed notes at `docs/releases/<tag>.md`.
It then runs the same checks on Linux and cross-builds macOS/Linux for amd64/arm64.
The build job has read-only repository access.
Only the publication job receives write access.

Assets contain four binaries, LICENSE and SHA256SUMS.
The publication job verifies checksums and uses the committed notes as its release body.
It publishes with `--prerelease --latest=false`.
After publication, a separate read-only job dispatches the exact version to
`alexei-led/homebrew-tap` with repository secret `TAP_GITHUB_TOKEN`.
The shared tap workflow calculates hashes from the published assets and updates the formula.
Scope that token to the tap with the permissions required for repository dispatch.
It is never used by PR checks or stored in source.
A successful dispatch is not proof that the downstream tap update completed.
Inspect both workflow results before claiming Homebrew availability.

Homebrew is the default installation path.
The formula installs only the prebuilt binary and has no service definition.
No Docker image or package registry is involved.

Before a release:

1. Add and review `docs/releases/<tag>.md`.
2. Merge the reviewed change only after PR CI succeeds.
3. Obtain explicit repository and tag publication approval.
4. Create the tag on that reviewed commit.
5. Inspect the workflow result and download the assets for checksum validation.

Do not create a tag merely to test this procedure.
The tag triggers external publication after the build gates pass.
Tag creation is the maintainer approval step.
No protected environment or second manual workflow approval is configured.
Release approval does not authorize a live provider call.
Current evidence and open gates belong in [verification](docs/verification.md#release-readiness).

## Documentation

| File                                   | Owns                                             |
| -------------------------------------- | ------------------------------------------------ |
| `README.md`                            | Front page: purpose, problem and visual overview |
| `docs/integration.md`                  | User guide: setup and rollback |
| `docs/configuration.md`                | Schema, authentication, HTTP behavior and limits |
| `docs/design.md`                       | Boundaries and design constraints                |
| `docs/verification.md`                 | Test evidence, gaps and readiness                |
| `docs/plans/implement-claude-proxy.md` | Remaining implementation work |
| `docs/releases/<tag>.md` | Canonical notes for each release |

Use short STE sentences in technical documents.
Keep commands, identifiers and quoted output unchanged.
Formal ASD-STE100 compliance needs the official dictionary and a full language review.
A style checker alone does not certify compliance.

The README diagram is hand-authored in `docs/assets/overview.svg`.
It has an accessible title, description and dark-theme rules.
After an edit, render it with `rsvg-convert` and inspect both themes.
Local image validation does not prove GitHub's final page layout.
