# Architecture

claude-proxy is a foreground process for one local profile.
It forwards native Anthropic requests to explicitly configured upstreams.
Model selection stays with the client or model-router.

See [configuration](configuration.md) for the wire contract, schema and limits.
See [integration](integration.md) for the user procedure.
See [verification](verification.md) for evidence and open acceptance gates.

## Design constraints

| ID  | Constraint                                                                          |
| --- | ----------------------------------------------------------------------------------- |
| D1  | One Go binary with standard-library production code                                 |
| D2  | Native Anthropic protocol on both sides, with no translation                        |
| D3  | Independent personal/work processes, listeners and credentials                      |
| D4  | Explicit destinations only, with no shared credential pool or account switching     |
| D5  | Model metadata, classifier calls and routing policy stay in model-router            |
| D6  | Exact native model IDs, with no proxy aliases or model rewriting                    |
| D7  | No retries, replay or provider fallback                                             |
| D8  | Loopback listener and a separate local access token                                 |
| D9  | Manual foreground operation, with no automatic startup or reload                    |
| D10 | Process-local client configuration, with existing login and profile files unchanged |

The proxy has no classifier, database, cache store, dashboard or provider SDK.
It does not manage OAuth login, refresh or account rotation.
It does not execute tools or add missing provider capabilities.

## Components

| Component                                 | Responsibility                                                    |
| ----------------------------------------- | ----------------------------------------------------------------- |
| `cmd/claude-proxy`                        | Flags, process signals and exit status                            |
| `internal/relay/config.go` and `files.go` | Strict configuration, private-file access and immutable snapshots |
| `internal/relay/auth.go`                  | Local token comparison and outbound credentials                   |
| `internal/relay/server.go`                | HTTP boundary, request limits and exact model lookup              |
| `internal/relay/catalog.go`               | Complete local model catalog                                      |
| `internal/relay/forward.go`               | ReverseProxy, shared transport and response filtering             |
| `internal/relay/process.go`               | Listener, request ownership and bounded shutdown                  |

One cohesive relay package owns the transport.
There is no plugin interface or general routing framework.

## Boundaries

### C1: model-router → Claude Code → proxy

Model-router selects a native ID and effort.
Claude Code constructs the request.
The proxy reads enough JSON to select an upstream, then forwards the original bytes.

The proxy does not interpret conversation history.
New client fields remain opaque unless they affect safe request framing.
Tool and thinking history can still be incompatible with the selected provider.

### C2: client configuration → proxy

The client uses one loopback URL and a separate local header.
Saved subscription OAuth remains the client's responsibility.
The proxy does not extract it from a credential store.

Profiles are separate operational boundaries, not a sandbox.
A same-user process can read both profiles' private files.
The user remains responsible for account selection and destination approval.

### C3: proxy → upstream

The configured model selects one explicit URL and authentication mode.
ReverseProxy removes hop-by-hop headers before credential injection.
OAuth routes retain session context.
Other routes receive only allowed protocol headers and their selected server key.

The transport uses system TLS trust.
It ignores ambient forward-proxy variables.
Requests have no replayable body.
The proxy neither follows redirects nor creates retry loops.

A response body is never accumulated as a full stream.
After response commitment, a transport error ends the connection.
The proxy never inserts an error body or synthetic success into a partial stream.

### C4: catalog → mixed router profile

The catalog contains configured native IDs and display names.
The mixed router profile holds metadata and routing rules.
The router does not import the catalog automatically.

The configuration examples deliberately duplicate native IDs across these files.
Inventory tests detect mismatches without a shared metadata schema.
The router's existing validator remains the owner of its full profile schema.

## Integration evidence

The external router source was inspected at
[version 1.17.1, commit 7e1274b](https://github.com/alexei-led/claude-router/tree/7e1274b69969bc4ddb09d7f12e3377e6afd030e1).

| Source                               | Relevant boundary                                                                  |
| ------------------------------------ | ---------------------------------------------------------------------------------- |
| `lib/config.mjs`                     | Model metadata, cache policy, classifier configuration and pure profile validation |
| `lib/providers.mjs`                  | Provider identifiers, file selection and native-ID context hints                   |
| `hooks/native-router.mjs`            | Client request control and the legacy gateway guard                                |
| `lib/cost.mjs` and `lib/savings.mjs` | Router-owned estimates, not provider invoices                                      |

Public client contracts are described in Anthropic's
[gateway documentation](https://code.claude.com/docs/en/llm-gateway#subscriptions-and-gateways)
and [gateway protocol](https://code.claude.com/docs/en/llm-gateway-protocol).
These sources do not replace live acceptance for the installed client version.

The integration guide records provider-file selection and discovery limits.
Classifier protocols remain outside this proxy.
Its optional development validator imports the trusted router source, not a runtime dependency.

## Risks

| ID  | Risk and stop condition                                                                        |
| --- | ---------------------------------------------------------------------------------------------- |
| R1  | Work history can contain company data. Do not send it to an unapproved destination             |
| R2  | Company TLS and OAuth context need live acceptance. Do not bypass managed restrictions         |
| R3  | Native API support does not prove tool, thinking, history or effort compatibility              |
| R4  | Saved OAuth and the custom local header need acceptance with the installed Claude Code version |
| R5  | Mixed billing and cache estimates can be inaccurate. Do not treat estimates as charges         |
| R6  | If reliable operation needs router source changes, stop and define separate work               |
| R7  | Configuration rotation needs a restart and can interrupt requests after the shutdown bound     |
| R8  | Fake-provider tests do not prove real account identity, OAuth refresh or provider behavior     |

CAUTION: Do not use rollback as a data-protection mechanism.
Rollback cannot retract history already sent or provider charges already incurred.

## Acceptance

Test definitions and current evidence are in [verification](verification.md#coverage).
Pending work is in the [implementation plan](plans/implement-claude-proxy.md).
No design note authorizes a live call, service installation or publication.
