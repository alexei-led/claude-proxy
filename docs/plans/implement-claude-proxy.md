# Implementation plan

This plan records remaining work.
The [architecture](../design.md) defines boundaries and risks.
The [verification report](../verification.md) owns test evidence and release gates.

## Status

| Stage | Task                                           | Status                                  |
| ----- | ---------------------------------------------- | --------------------------------------- |
| 1     | 1: strict configuration and authentication     | Implemented                             |
| 1     | 2: native HTTP relay and complete catalog      | Implemented                             |
| 2     | 3: public Claude Code/model-router integration | Examples and offline checks implemented |
| 3     | 4: broader reliability, fitness and benchmarks | Pending                                 |
| 4     | 5: final acceptance and release preparation    | Pending                                 |

Tasks 1–3 do not establish live provider support.
Publication requires separate authorization.
The current authorization covers the initial main bootstrap and the `v0.1.0` prerelease, not live provider calls.

## Task 4: Reliability and performance

Contracts: D1/D3/D7/D8, C1–C4, T4–T10/F1–F2.

Existing tests already cover many resource and transport cases.
Extend them only where a required behavior lacks evidence.

- [ ] Map test gaps before adding cases.
- [ ] Exercise growing histories across multiple providers.
- [ ] Exercise simultaneous long streams in separate profiles.
- [ ] Repeat cancellation, slot release and bounded shutdown cases.
- [ ] Add architecture checks for standard-library dependencies and forbidden side effects.
- [ ] Measure direct versus relayed requests with 8 KiB, 1 MiB and near-limit bodies.
- [ ] Measure concurrency levels 1 and 8.
- [ ] Record latency, allocations, retained memory and chunk timing in the verification report.

Benchmarks use fake providers.
Report proxy overhead separately from provider latency.
Do not add dependencies or weaken safety limits for an unmeasured speed target.

## Task 5: Final acceptance

Contracts: T1–T11/F1–F2 and R1–R8.

- [ ] Run the [development checks](../../CONTRIBUTING.md#build-and-tests).
- [ ] Run hosted CI on the reviewed commit.
- [ ] Make sure that the external router source remains unchanged.
- [ ] Obtain approval for each live account, destination, data set and cost.
- [ ] Run personal subscription OAuth and refresh acceptance.
- [ ] Run company OAuth, headers and TLS acceptance separately.
- [ ] Run approved mixed-provider sessions with actual router decisions.
- [ ] Exercise tools, thinking, history, effort, subagents and context hints.
- [ ] Record actual model identity and billing behavior.
- [ ] Exercise discovery limits without model renaming or replacement credentials.
- [ ] Stop the proxy and make sure that opted-in requests fail without fallback.
- [ ] Restore the original client connection configuration and router profile.
- [ ] Record every unrun case and exclude unsupported combinations.

A live failure does not permit body translation, credential substitution or fallback.
Define separate work if the current protocol cannot support the required behavior.

## First experimental release

- [x] Resolve security findings and run their regression tests.
- [ ] Create reviewed commits only after authorization.
- [ ] Complete the non-default-branch review and merge process.
- [ ] Pass hosted CI on the release commit.
- [ ] Complete the minimum live acceptance in the verification report.
- [x] Set explicit prerelease publication for experimental tags.
- [ ] Inspect the four binary assets, license and checksums.
- [x] Obtain explicit publication approval for `v0.1.0` as a prerelease.

Use an experimental prerelease label for the first public version.
Do not mark a version stable while required live gates remain open.
The current recommendation is in [release readiness](../verification.md#release-readiness).

## Working rules

Use a non-default branch and a worktree when a committed base exists.
Preserve unrelated edits.
Do not create a commit solely to provide a worktree base without authorization.

Automated tests use synthetic history, local fake providers and dummy credentials.
They do not read saved OAuth stores or probe real providers.
Live acceptance remains an explicit, separate operation.

There is no valid GitNexus index for the current code.
Use source reads and tests for evidence.
Ordinary Git diffs do not include untracked files.

Keep original user profiles and managed configuration unchanged.
The [integration guide](../integration.md#stop-and-rollback) owns the rollback procedure.
