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
```

`--check` performs file validation without a listener or network calls.
Normal operation prints a non-secret `ready` record after the socket opens.
SIGINT or SIGTERM starts bounded shutdown.

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
| 504    | Upstream response-header timeout                                           |

The standard HTTP server can reject framing before the handler runs.
Those responses use the standard server format, including 431 for excessive headers.

## Resource limits

These are implementation constants, not configuration fields.

| Resource                      | Limit                                          |
| ----------------------------- | ---------------------------------------------- |
| Request body                  | 32 MiB                                         |
| Concurrent forwarded POSTs    | 8, with immediate rejection of excess requests |
| Inbound headers               | 64 KiB configured server limit                 |
| Upstream response-header size | 64 KiB                                         |
| Header read                   | 5 seconds                                      |
| Body read                     | 30 seconds                                     |
| Dial / TLS handshake          | 10 seconds each                                |
| Upstream response-header wait | 180 seconds                                    |
| Graceful shutdown             | 30 seconds, then active connections close      |

There is no whole-generation or stream-idle timeout.
A stalled authenticated stream can hold a slot until cancellation or shutdown.
Catalog requests do not use generation slots.
A disconnected client cancels its upstream request.

## Logs

Logs contain readiness and bounded request summaries.
Fields include the profile, method/path kind, configured model/upstream label,
status, elapsed time, byte counts and a fixed local error category.

Model labels have a 128-byte log bound.
Upstream labels have the schema bound stated earlier.
Logs contain no bodies, secrets, header values, secret paths or full upstream URLs.
The proxy creates no persistent log file.
