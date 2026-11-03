# Configuration reference

One process loads one configuration and its referenced secrets.
The configuration and keys remain in memory until the process stops.
Changes require a restart.

See [integration](integration.md) for the procedure.
See the [examples](../examples/) for complete JSON files.

## Command line

```text
claude-proxy --config PATH
claude-proxy --config PATH --check
claude-proxy run --config PATH -- COMMAND [ARGS]
claude-proxy connect --config PATH -- COMMAND [ARGS]
claude-proxy status --config PATH
claude-proxy stop --config PATH
```

`--check` performs file validation without a listener or network calls.
Foreground operation prints readable startup, address and request lines. Terminal output is colored unless `NO_COLOR` is set; redirected output is plain text. Detached managed brokers retain bounded JSON logs with problems and traffic summaries only. All formats omit credentials and bodies. See [Logs](#logs).
SIGINT or SIGTERM starts bounded shutdown.

`run` starts or reuses a registered server for this configuration and installed binary, including a matching manually started foreground server. It registers its PID before replacing itself with the client. The broker binds a free literal loopback port; `listen` remains the standalone-mode address. A process waiting for input or running tools still counts as a session.

New configuration, selected key contents or executable bytes create a new generation. Existing clients keep their old broker. No active stream is restarted and no provider request is retried. `status` reports the current generation's actual address, mode (`manual` or `managed`), version, PID, sessions, active requests, tracking uncertainty and [traffic stats](#stats) without secrets. `stop` refuses while clients or requests remain active.

macOS tracks process exits with kqueue. Linux records the process birth identity and checks exit/zombie state. Unknown liveness prevents idle shutdown. Detached descendants must register through their own launcher; scanning unrelated processes is not supported.

`connect` is an optional process-local client helper. It does not start a listener.
It reads the configuration and local token without opening upstream key files, checks the complete local catalog, and replaces its process with the supplied command.
The child receives `ANTHROPIC_BASE_URL`, the separate access header and `CLAUDE_ROUTER_PROVIDER=model-proxy`.
The parent environment and saved OAuth remain unchanged. Resolve conflicting client settings and policies before use.
See [client setup](integration.md#6-start-the-opted-in-session) for constraints and the manual alternative.

## JSON schema

The maximum configuration size is 1 MiB.
The maximum JSON depth is 100 levels.
The parser rejects invalid UTF-8, duplicate keys and unknown fields.
Field names are case-sensitive.
The parser rejects null values, extra JSON values and unsupported versions.

| Field               | Required | Meaning                                                  |
| ------------------- | -------- | -------------------------------------------------------- |
| `version`           | Yes      | Integer `1`                                              |
| `profile`           | Yes      | `personal` or `work`                                     |
| `listen`            | Yes      | Literal `127.0.0.1` and an explicit port from 1 to 65535 |
| `access_token_file` | Yes      | File that contains this profile's local access token     |
| `upstreams`         | Yes      | Nonempty object of named upstreams                       |
| `models`            | Yes      | Nonempty object of exact native model IDs                |
| `runtime`           | No       | Idle timeout, forwarding concurrency, header wait and log level |

### Discovery and ports

Both foreground and managed servers register in the same private control registry. Matching means the canonical configuration path, immutable configuration/credential snapshot and installed executable match; the catalog alone is not proof of identity. Servers from older versions without foreground registration need one restart after upgrading. A missing current-generation registration does not prove an older proxy is stopped. In that case `status` makes a bounded TCP-only observation of the configured listener, sends no credentials or HTTP request, and clearly labels an occupied port as unverified. Older managed generations may use other ports. Unknown processes listening on a port are never adopted or killed.

Managed servers bind a free loopback port. Foreground servers bind `listen`. The launcher asks the registry for the actual address and passes it only to its child; no port edits or guessing are needed. Different configurations have separate identities and local tokens. For multiple foreground configurations, select distinct `listen` ports; a collision fails clearly rather than attaching to another config.

A repeated foreground start reports an existing matching server and exits without creating a duplicate or changing its ownership mode. A managed server remains managed; a manual server remains manual. Manually started servers never auto-close, even when the config includes a managed idle timeout. Use Ctrl-C/SIGTERM or an explicit idle-only `stop` command to stop them. Stopping a manual server can affect clients attached to it.

### Runtime settings

```json
{
  "runtime": {
    "idle_timeout": "5m",
    "max_inflight": 32,
    "response_header_timeout": "180s",
    "log_level": "info"
  }
}
```

`log_level` is optional: `trace`, `debug`, `info`, `warn` or `error`. Without it, a foreground terminal uses `debug`, which shows each successful generation, and a detached managed broker uses `info`, which keeps problems and summaries only. See [Logs](#logs) for what each level adds.

`response_header_timeout` defaults to `180s` and accepts 1 second to 10 minutes. It is how long one upstream try may take to start answering. Raise it for a provider that queues requests under load. It covers the wait after the request was sent; a dial or TLS handshake that stalls fails with 504 after its own 10 seconds.

`idle_timeout` defaults to `5m`; `0` disables managed idle shutdown. The timer runs only with zero living client registrations and zero active forwarded requests. New registrations or forwarding reset it. Unknown process tracking keeps the broker alive. Manually started servers do not use this timer.

`max_inflight` defaults to 8 and accepts integers 1 through 64. It controls forwarding admission and per-host transport connections in both modes. Excess requests receive 429 immediately; there is no hidden queue. The examples use 32 after a fake-provider check with 32 simultaneous 1 MiB bodies. At the 32 MiB per-request ceiling, 32 admitted bodies alone can require about 1 GiB before other allocations. Provider limits still apply.

Managed runtime files live in an owner-only `claude-proxy` directory below the OS user cache directory, or `XDG_RUNTIME_DIR` when set. Locks, control sockets and bounded private logs are machine state, not configuration. The selected runtime parent must be absolute and the control socket path must fit within 100 bytes. Set a shorter private `XDG_RUNTIME_DIR` if needed.

Control messages contain process and generation metadata and traffic counts, never API keys or OAuth. Each generation keeps its current log and one previous `.log.1` file, each at most 1 MiB. Stopped brokers may leave stale sockets/locks/logs; the startup-lock owner safely replaces a stale socket. Do not delete runtime files while brokers are running.

Listener hostnames, wildcard addresses and leading-zero ports are invalid.
Each profile needs its own listener, token and provider keys.
No environment interpolation, inline secrets or shell commands are supported.

### Upstreams

An upstream label contains at most 64 ASCII letters, digits, hyphens or underscores.
The label is not a model prefix.

| Field              | Required             | Meaning                                             |
| ------------------ | -------------------- | --------------------------------------------------- |
| `messages_url`     | Yes                  | Complete URL for native Anthropic Messages requests |
| `count_tokens_url` | No                   | Complete URL for native token-count requests        |
| `auth.mode`        | Yes                  | One of the modes in the next table                  |
| `auth.key_file`    | For server-key modes | File that contains the selected provider's key      |
| `retry`            | No                   | `{"max_attempts": N}`, see below                    |

`retry.max_attempts` is the total number of tries per request, from 1 to 5. Omit `retry`, or use 1, for no retry. The relay re-sends only after an upstream 503 or 529 answer, or a failed connection dial, and waits 0.25 to 4 seconds between tries with jitter. It honors a `Retry-After`, in seconds or as an HTTP date, of up to 4 seconds and passes a longer one to the client. It never retries a timeout or a reset after the request was sent. The request log shows `retried ×N` and the cause of each retry, such as `(529,529)`. Each try gets its own `response_header_timeout`.

Endpoint URLs need an absolute path and host.
HTTPS is required, except for HTTP at a literal loopback IP.
User information, query strings and fragments are invalid.
The proxy does not infer paths, add `/v1` or follow redirects.
It uses system TLS trust and ignores ambient HTTP proxy variables.

### Models

Each key in `models` is an exact, nonempty native API model ID.
Each entry requires `upstream`, which names one configured upstream.
An optional `display_name` changes only the catalog label.
If that label is absent or empty, the catalog uses the ID.

One native ID maps to one upstream per profile.
Duplicate IDs are invalid, even if the upstreams differ.
The proxy does not remove `[1m]` hints or substitute aliases.
Prices, capabilities, efforts and routing rules do not belong in this file.

## Authentication

Every request needs one `X-Claude-Proxy-Key` header with the selected local token.
The proxy compares token hashes in constant time.
It removes this header before the upstream request.

| Mode                | Key file | Outbound credential                                  |
| ------------------- | -------- | ---------------------------------------------------- |
| `oauth_passthrough` | None     | The client's single OAuth Bearer Authorization value |
| `bearer`            | Required | `Authorization: Bearer` with the server-held key     |
| `x_api_key`         | Required | `X-Api-Key` with the server-held key                 |
| `none`              | None     | No credential                                        |

OAuth routes retain the client protocol, app and organization context.
They remove conflicting API keys, cookies and proxy credentials.
Server-key and unauthenticated routes use this header allowlist:

- `Content-Type`
- `Accept`
- `Anthropic-Version`
- `Anthropic-Beta`
- `User-Agent`

These routes receive no client OAuth, device, workspace or organization headers.
All routes still receive the original body, including identity metadata.
These routes lose only the `pattern` keywords in `tools[].input_schema`; see [HTTP endpoints](#http-endpoints).

The server accepts only the configured Host and port.
It rejects Origin, browser Fetch Metadata, cookies, proxy credentials and upgrades.
It also rejects duplicate security-sensitive headers.
There is no CORS support.

The query filter rejects these reserved parameter names without case sensitivity:
`authorization`, `auth`, `api_key`, `api-key`, `apikey`, `x-api-key`,
`access_token`, `token`, `key` and `x-claude-proxy-key`.
The filter decodes parameter names and recognizes ampersand or semicolon separators.

Known credential headers and Set-Cookie do not return to the client.
This rule includes 1xx responses and announced or unannounced trailers.
Safe end-to-end headers and trailers remain intact.

### Private files

Relative paths resolve against the configuration directory.
Absolute paths are allowed.
Paths do not expand `~` or environment variables.

| File                        | Requirements                                                                 |
| --------------------------- | ---------------------------------------------------------------------------- |
| Configuration               | Current-user-owned regular file, no symlink, no group/other write permission |
| Local token or provider key | Current-user-owned regular file, no symlink, mode 0400 or 0600               |

Use a private parent directory and mode 0600 for the configuration.
Each secret needs printable ASCII bytes and a maximum size of 16 KiB.
One trailing LF or CRLF is allowed.
Empty values, embedded controls and a trailing bare CR are invalid.

The proxy reads only files referenced by the selected configuration.
It does not read Claude credential stores, Keychain or another process's environment.

## HTTP endpoints

| Request                          | Result                                               |
| -------------------------------- | ---------------------------------------------------- |
| `GET /v1/models`                 | Complete sorted local catalog                        |
| `POST /v1/messages`              | Request to the exact configured Messages URL         |
| `POST /v1/messages/count_tokens` | Request to the configured counting URL, or local 404 |

The catalog accepts `limit=1000` but does not paginate.
It returns native IDs, display names and `has_more: false`.
It uses `Cache-Control: no-store` and makes no upstream call.

POSTs require `application/json` and identity or absent Content-Encoding.
A request needs one top-level, nonempty string `model`.
Malformed JSON and duplicate or case-conflicting model fields are invalid.

The proxy forwards original body bytes, raw queries and upstream response bytes.
On `bearer`, `x_api_key` and `none` routes it first removes every `pattern` schema keyword from tool input schemas,
because third-party providers reject regexes Anthropic accepts.
A property named `pattern` and data keywords such as `enum` or `default` stay intact.
A request whose tool schemas hold no pattern keeps its original bytes.
It does not parse SSE events, decompress responses or change native model IDs.
An upstream 3xx, 4xx or 5xx response remains an upstream response.
A broken stream ends without a synthetic completion or replay.

### Local errors

Handler errors use the Anthropic-style `type: error` envelope.
Messages contain fixed text, not request values or secrets.

| Status | Cause                                                                      |
| ------ | -------------------------------------------------------------------------- |
| 400    | Invalid JSON, model field, credentials or request framing                  |
| 401    | Missing, wrong or duplicate local key, or missing OAuth for an OAuth route |
| 403    | Host mismatch or browser-origin request                                    |
| 404    | Unknown path/model or unavailable counting route                           |
| 405    | Unsupported method on a known path                                         |
| 413    | Request body exceeds its limit                                             |
| 415    | Unsupported media type or request encoding                                 |
| 429    | All forwarding slots are occupied                                          |
| 502    | Upstream connection, TLS or protocol error                                 |
| 499    | The client hung up before the upstream answered (logged only)              |
| 504    | Upstream dial, TLS handshake or response-header timeout                    |

The standard HTTP server can reject framing before the handler runs.
Those responses use the standard server format, including 431 for excessive headers.

## Resource limits

Most limits are implementation constants. Forwarding concurrency is configurable through `runtime.max_inflight`, and the upstream response-header wait through `runtime.response_header_timeout`.

| Resource                      | Limit                                          |
| ----------------------------- | ---------------------------------------------- |
| Request body                  | 32 MiB                                         |
| Concurrent forwarded POSTs    | Default 8; configurable 1–64, immediate rejection of excess requests |
| Inbound headers               | 64 KiB configured server limit                 |
| Upstream response-header size | 64 KiB                                         |
| Header read                   | 5 seconds                                      |
| Body read                     | 30 seconds                                     |
| Dial / TLS handshake          | 10 seconds each                                |
| Upstream response-header wait | Default 180 seconds; configurable 1s–10m, per try |
| Graceful shutdown             | 30 seconds, then active connections close      |

There is no whole-generation or stream-idle timeout.
A stalled authenticated stream can hold a slot until cancellation or shutdown.
Catalog requests do not use generation slots.
A disconnected client cancels its upstream request.

## Logs

Logs record lifecycle events, one record per request that deserves attention, and a periodic traffic summary. The level of a request record depends on how the request ended:

| Level   | Records                                                                                |
| ------- | -------------------------------------------------------------------------------------- |
| `warn`  | Any failure: local rejection, capacity 429, provider error, timeout, broken stream     |
| `info`  | Lifecycle, 5-minute summaries, requests that succeeded after a retry, client hang-ups |
| `debug` | Each successful generation                                                             |
| `trace` | Successful catalog and token-count requests                                            |

A setting shows its level and everything above it. The defaults are `debug` in a foreground terminal and `info` in a managed broker; `runtime.log_level` overrides both.

### Request records

Every record has the profile, method, path kind (`messages`, `count_tokens`, `models` or `unknown`), status, outcome, elapsed time and byte counts. Other fields appear only when they apply:

| Field                                                                       | Meaning                                                                                    |
| --------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| `outcome`                                                                   | `ok`, `rejected`, `busy`, `upstream_error`, `upstream_unreachable`, `timeout`, `upstream_broken` or `client_closed` |
| `model`, `upstream`                                                         | Configured labels of the route                                                             |
| `category`                                                                  | Error type of a response the relay generated itself                                        |
| `ttfb`                                                                      | Time until the provider's response headers reached the client, retries included           |
| `retries`, `retry_reasons`                                                  | Retry count and the cause of each: an upstream status or `dial`                            |
| `upstream_request_id`                                                       | The provider's `request-id` or `x-request-id`; quote it to provider support                |
| `error_type`                                                                | Provider error type from an error body or a streamed `error` event                         |
| `stop_reason`                                                               | Why the generation ended, such as `end_turn` or `max_tokens`                               |
| `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens` | Usage the provider reported for a Messages response                                       |

`upstream_broken` means the provider cut a response after it began, for example a stream that stopped mid-generation. Its status stays 200 because the headers were already sent; before this outcome existed, such a stream looked like a success. A stream with an `error` event is `upstream_error`, also with status 200.

The terminal format shows the same facts on one line and lists only what is unusual: the outcome when it is not `ok`, the time to first byte, token counts, a stop reason other than a normal end, retries with their causes, and the provider request ID of a failed request.

### Stats

Each server counts requests per path, model and upstream from its start: outcomes, retries, tokens, and the 50th and 95th percentiles of time to first byte and of forwarded-request duration. Percentiles are upper bounds of fixed buckets from 100 ms to 5 minutes.

- `status` returns the counts since start in its `stats` field.
- Every 5 minutes, and once at shutdown, the log gets one `info` summary per route that had traffic in that period. Catalog reads are left out.

### Content limits

Model labels have a 128-byte log bound.
Upstream labels have the schema bound stated earlier.
Provider request IDs, error types and stop reasons are logged only when they are short tokens of safe characters.
Logs contain no bodies, secrets, other header values, secret paths or full upstream URLs.
A foreground server writes no log file. A managed broker writes only its bounded private log.
