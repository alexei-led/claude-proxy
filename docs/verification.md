# Tests and release readiness

This report separates local evidence from unrun live acceptance.
A passing fake-provider test does not prove a real provider or client combination.

## Release readiness

**Local, controlled experiments:** appropriate after the local checks pass.
Use non-sensitive synthetic data and explicitly approved destinations.

**Experimental publication:** `v0.1.0` uses GitHub's prerelease state.
Its purpose is controlled experimentation and CI/release pipeline validation.
It is not evidence of actual provider compatibility.

**Stable release:** not ready.
Provider compatibility, account identity, billing and broader reliability evidence remain incomplete.

The release workflow runs the local gates again before publication.
It uses committed notes and explicit `--prerelease --latest=false` flags.
Hosted results are recorded in the
[CI runs](https://github.com/alexei-led/claude-proxy/actions/workflows/ci.yaml)
and [release runs](https://github.com/alexei-led/claude-proxy/actions/workflows/release.yaml).

A release does not satisfy the unrun live gates.
Use only non-sensitive data until each account/provider combination passes approved acceptance.
A later stable release needs hosted CI, real OAuth/provider evidence and binary inspection.

## Local environment

| Tool                          | Version                  |
| ----------------------------- | ------------------------ |
| Go                            | 1.27.3, darwin/arm64     |
| Module language               | Go 1.27.0                |
| golangci-lint                 | 2.15.0                   |
| Node.js for router validation | 24.15.0                  |
| External router source        | 1.17.1, commit `7e1274b` |

The proxy has no third-party Go modules.
The external router source was not edited by this work.
Automated checks used fake providers and dummy credentials.
Approved live checks are recorded separately below.
No service or automatic startup was installed.

## Recorded checks

| Check                                        | Evidence                                                                               |
| -------------------------------------------- | -------------------------------------------------------------------------------------- |
| Formatting and import formatting             | Local Makefile checks passed                                                           |
| Lint, including Go tests                     | Zero issues                                                                            |
| `go vet`                                     | Passed                                                                                 |
| Unit and contract tests                      | Passed against fake providers                                                          |
| Race detector                                | Passed                                                                                 |
| Native build                                 | Passed                                                                                 |
| macOS/Linux amd64/arm64 cross-builds         | Cross-compiled, not executed on non-host targets                                       |
| Repeated integration cases                   | Ten repetitions passed                                                                 |
| Example configuration                        | Both profiles passed `--check` with temporary owner-only dummy files                   |
| Mixed router schema                          | Existing router validator accepted the four-model profile                              |
| Invalid router profile and missing arguments | The development validator rejected both                                                |
| Module inventory                             | `go list -m all` lists only the proxy module                                           |
| Workflow validation                          | actionlint and zizmor auditor checks passed during initial verification                |
| Git hook validation                          | ShellCheck, shfmt and temporary-repository fixtures passed during initial verification |
| Secret scan                                  | Gitleaks source scan passed with redacted output                                       |
| Documentation                                | Relative links passed, with no broken links                                            |

Run commands are in [development](../CONTRIBUTING.md#build-and-tests).
The test suite does not contact real providers.
Documented client commands use fake `claude` and `curl` functions during tests.

## Coverage

| Gate | Required behavior                       | Current evidence                                                                                             |
| ---- | --------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| T1   | Strict configuration and private files  | Schema, duplicates, UTF-8, file modes, symlinks, snapshots and check mode                                    |
| T2   | Local HTTP boundary                     | Profile key, Host, browser headers, methods, paths, body/header limits and reserved query names              |
| T3   | Native request fidelity                 | Exact body, model ID, URL path and raw query, including semicolons                                           |
| T4   | Credential isolation                    | OAuth/server-key/no-auth modes, hostile Connection headers and separate profile captures                     |
| T5   | Streaming                               | Early headers/chunks, opaque events, truncation and disconnect cancellation                                  |
| T6   | Failures                                | Upstream status/body, redirects, TLS rejection, header timeout and accepted-generation non-replay            |
| T7   | Resource ownership                      | Overload, slot release, race tests and bounded shutdown                                                      |
| T8   | Catalog                                 | Complete sorted native IDs, mixed-profile inventory and no upstream calls                                    |
| T9   | Public configuration procedure          | Profile-specific commands, no overwrite, reserved-header rejection and local catalog inspection              |
| T10  | Mixed-provider workflow                 | Five scripted turns across four fake upstreams in each profile                                               |
| T11  | Actual Claude Code/provider integration | Scoped live acceptance below, not full compatibility                                                                                                      |
| F1   | Dependency and privacy fitness          | Standard-library module inventory and focused behavior tests, no separate architecture test yet              |
| F2   | Scope fitness                           | Behavior tests cover no rewriting, replay, fallback or automatic startup, broader scope tests remain pending |

T7 and T10 do not establish large-history or long-stream capacity guarantees.
Benchmarks and dedicated architecture checks remain open.

## Regression evidence

The implementation work reproduced and corrected these boundary errors:

- Credential headers in informational 1xx responses.
- Credential values in unannounced trailers.
- Rejection of valid raw semicolon queries.
- Replacement of invalid UTF-8 during JSON decoding.
- Acceptance of `apiKey` and `apikey` query parameters.
- Personal connection values in draft work-profile instructions.
- Partial configuration copies after a failed no-overwrite guard.

Regression tests cover these cases.
The query-name fix also covers percent-encoded names and semicolon-separated parameters.

## Live acceptance

These scoped checks ran on 2026-10-30 with explicit operator approval.
They used the released v0.1.0 proxy, Claude Code 2.1.314 and router Mod 1.18.0.
Requests contained synthetic prompts or a synthetic local file, not project code.
The results describe the tested credentials and models, not universal provider support.

| Model | Observed result |
| --- | --- |
| `claude-haiku-5-5` | Existing personal OAuth and the separate local header worked through Claude Code |
| `deepseek-flash` | Native SSE, Claude Code replies, Read-tool completion and context hints worked |
| `glm-5.3-flash` | Native SSE, Claude Code replies, Read-tool completion and context hints worked |
| `mimo-v2.6-flash` | Native SSE, Claude Code replies and Read-tool completion worked |
| `kimi-k2.7-code` | Native SSE, Claude Code replies and Read-tool completion worked |
| `MiniMax-M3` | The tested key returned 401, `token is unusable`, directly and through the proxy |

No proxy authentication fix was needed for the MiniMax result.
The ready experimental configuration excludes that unusable credential.

### Routing and history

One Claude Code conversation completed DeepSeek → GLM → DeepSeek.
The actual router Mod selected each native ID from controlled local classifier advice.
A separate interactive session used the existing Jev classifier successfully.
Tier pins then switched that live session to GLM and back to DeepSeek.

DeepSeek signed thinking history was accepted by GLM in a scoped native request.
GLM tool/thinking history was accepted by MiMo.
MiMo then returned thinking after text, which Kimi rejected on replay.
The proxy returned that error unchanged.
It did not reorder or remove content to force compatibility.

Explicit `thinking: disabled` requests were also rejected by GLM or Kimi in tested cases.
Normal Claude Code sessions still completed through those providers.
Provider parameter subsets and history ordering remain real compatibility limits.
The ready automatic profile uses the tested DeepSeek/GLM pair.

### Client behavior

Claude Code reported a 200K window for bare third-party IDs.
With `[1m]` metadata hints, it reported 1M and sent bare native IDs to the proxy.
Only that client metadata behavior was verified, not full-window inference.
The corrected router example preserves those hints outside the proxy catalog.

The client also warned about unrecognized third-party IDs.
Successful requests still completed.
Its usage records labeled those routes `firstParty` with `costBasis: unknown`.
Do not treat the displayed dollar estimates as actual provider invoices.

One DeepSeek client request stalled and was canceled by the bounded test harness.
Later requests succeeded, so no deterministic proxy defect was established.
The native client retried failed MiniMax requests independently.
The proxy itself added no retry or fallback.

### Cancellation and configuration safety

A live stream was closed after its initial event.
The proxy request ended promptly, and a later catalog request and completion succeeded.
This does not prove when the provider stopped billing.

Relevant existing configuration files were hashed before and after the run.
They remained unchanged.
The operator used new private, opt-in files and a manual foreground process.
No work-account acceptance, logout or managed-policy edit was performed.
OAuth expiry and refresh were not forced or certified.

## Unrun acceptance

These checks require separate approval or an executable hosted pipeline:

- Hosted run outcomes are tracked by the workflow links in the readiness section.
- Expired OAuth refresh and exhausted-account recovery. Initial OAuth/custom-header coexistence passed the scoped live check.
- Company TLS trust, proxy headers and account identity.
- Provider combinations and features beyond the exact live cases recorded above.
- Subagent behavior across providers. Main-session router and classifier paths passed the scoped live check.
- Native picker discovery and custom-header-only behavior.
- Actual provider usage and billing.
- Long simultaneous streams, growing histories and transport benchmarks.
- Execution of non-host binaries.
- Local-model inference. Ollama was not running, and no model weights were installed.

No test result here authorizes company data transmission or provider spending.
Risks are defined in the [architecture](design.md#risks).

## Documentation checks

Technical prose uses short STE sentences and stable technical terms.
Formal ASD-STE100 compliance is not certified without the official dictionary and a full review.

The README uses a hand-authored SVG with light and dark theme rules.
Local raster previews permit layout inspection.
GitHub page rendering needs a final inspection after source publication.
