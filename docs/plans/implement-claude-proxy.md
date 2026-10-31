# Remaining work

This plan lists implementation and acceptance gaps.
The [verification report](../verification.md) owns evidence and release status.
The [architecture](../design.md) owns boundaries and risks.
[CONTRIBUTING](../../CONTRIBUTING.md#ci-and-release) owns the release procedure.

## Completed scope

- Strict configuration, private-file rules and authentication.
- Native HTTP forwarding, streaming and a complete local catalog.
- Public Claude Code setup and mixed model-router examples.
- Scoped live acceptance and client context-hint corrections.

These results do not establish universal provider compatibility.
Temporary live-test configuration and credentials were removed after acceptance.
A fresh setup uses the checked-in examples and the user guide.

## Reliability and performance

Contracts: D1/D3/D7/D8, C1–C4, T4–T10/F1–F2.

Existing tests cover basic overload, cancellation, slot release and bounded shutdown.
Remaining work must extend that evidence, not duplicate the same cases.

- [ ] Exercise growing histories across multiple providers.
- [ ] Exercise simultaneous long streams in separate profiles.
- [ ] Add dedicated architecture checks for dependencies and forbidden side effects.
- [ ] Measure direct and relayed requests with 8 KiB, 1 MiB and near-limit bodies.
- [ ] Measure concurrency levels 1 and 8.
- [ ] Record latency, allocations, retained memory and chunk timing in the verification report.

Use fake providers for benchmarks.
Separate proxy overhead from provider latency.
Do not weaken safety limits to meet an unmeasured speed target.

## Broader live acceptance

Contracts: T11 and R1–R8.

The exact completed cases belong in the verification report.
The following gaps remain:

- [ ] Retest MiniMax through the proxy with a valid replacement credential.
- [ ] Exercise expired OAuth refresh and exhausted-account recovery.
- [ ] Exercise approved company OAuth, headers, TLS and account identity.
- [ ] Exercise subagents across providers.
- [ ] Exercise native picker discovery and custom-header-only behavior.
- [ ] Exercise full-window requests and untested tool/thinking-history combinations.
- [ ] Compare actual provider usage and bills with client/router estimates.
- [ ] Run local-model inference and non-host release binaries.

Obtain separate approval for each live account, destination, data set and cost.
No plan item authorizes credential access, provider spending or publication.

A provider error is not permission to translate bodies, substitute credentials or add fallback.
If reliable operation needs router source changes, define separate work.
Use the [user guide](../integration.md#stop-and-rollback) for rollback.
