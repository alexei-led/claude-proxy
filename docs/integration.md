# Configure Claude Code and model-router

Use this procedure for a manually started local proxy.
No extra launcher is required.
Read [release readiness](verification.md#release-readiness) before a real session.

CAUTION: Authorize every destination before use.
The selected provider receives the full unchanged conversation history.

## 1. Prepare the client and examples

1. Build `bin/claude-proxy` with the [development procedure](../CONTRIBUTING.md#build-and-tests).
2. Install Claude Code and model-router through their public instructions.
3. Select the account's existing Claude Code configuration directory.
4. Keep its saved OAuth login unchanged.
5. Use native Anthropic mode.
6. Resolve conflicting API-key helpers, inherited provider configuration and managed policies.
7. Do not bypass a policy to enable the proxy.

| Example                                            | Purpose                                                         |
| -------------------------------------------------- | --------------------------------------------------------------- |
| [personal.json](../examples/personal.json)         | Direct Anthropic subscription and separately declared providers |
| [work.json](../examples/work.json)                 | Company endpoint placeholder and independent provider keys      |
| [proxy-router.json](../examples/proxy-router.json) | Four native models and explicit tier routes                     |

Make sure that every endpoint and model ID matches an authorized destination.
The company hostname is an unresolvable placeholder.
Remove unauthorized destinations from both the proxy and mixed router profile.

Hosted metadata comes from the inspected router presets, not fresh provider measurements.
The local model assumes text-only input, a 32K context window and no per-token charge.
The strengths and tier assignments are examples, not benchmark results.
Replace those assumptions before automatic routing.

The example disables activity/subagent routing and cache-cost assumptions.
It does not disable upstream caches or remove cache markers.

### Classifier configuration

The example uses model-router's existing Ollama classifier.
Its preset uses `http://127.0.0.1:11434/api/chat` and `qwen3.5:9b`.
The execution model `qwen3-coder:30b` is separate.

Prepare that classifier yourself or select another supported classifier in model-router.
Remote classifier credentials remain in the router's plugin options.
The proxy handles neither classifier requests nor classifier credentials.

## 2. Select the profile

For personal:

```sh
proxy_profile=personal
```

For work, select its existing Claude Code configuration directory:

```sh
proxy_profile=work
export CLAUDE_CONFIG_DIR=/path/to/existing/work-profile
```

Make sure that the directory belongs to the intended account.
Run this selection block in each terminal used by this procedure:

```bash
: "${proxy_profile:?Select personal or work first}"
config_root="${XDG_CONFIG_HOME:-$HOME/.config}/claude-proxy"
case "$proxy_profile" in
  personal) proxy_base_url=http://127.0.0.1:43171 ;;
  work)
    : "${CLAUDE_CONFIG_DIR:?Select the existing work Claude Code config directory}"
    proxy_base_url=http://127.0.0.1:43172
    ;;
  *) printf 'Unknown proxy profile. Stop.\n' >&2; exit 1 ;;
esac
proxy_config="$config_root/$proxy_profile.json"
proxy_token_file="$config_root/secrets/$proxy_profile-access.key"
claude_dir="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
```

The block uses the example listeners and token filenames.
If you change those values, update the block to match.
Keep the listener loopback-only.

## 3. Prepare private configuration

Run this copy operation from the project directory.
It stops if a destination exists, including a dangling symlink.

```bash
(
  set -eu
  umask 077
  mkdir -p "$config_root/secrets" "$claude_dir"
  if test -e "$proxy_config" || test -L "$proxy_config"; then
    printf 'Selected proxy configuration already exists. Stop.\n' >&2
    exit 1
  fi
  if test -e "$claude_dir/proxy-router.json" || test -L "$claude_dir/proxy-router.json"; then
    printf 'Selected router profile already exists. Stop.\n' >&2
    exit 1
  fi
  cp -n "examples/$proxy_profile.json" "$proxy_config"
  cp -n examples/proxy-router.json "$claude_dir/proxy-router.json"
)
```

The selected profile supplies its configuration, token path and listener.
Do not combine values from different profiles.

Generate only the selected local token:

```sh
(
  set -eu
  umask 077
  set -C
  openssl rand -hex 32 > "$proxy_token_file"
)
```

The command refuses to overwrite an existing token.
Save provider-issued keys in the referenced private files.
Do not generate substitute provider keys or put keys in the router profile.

Use the [private-file rules](configuration.md#private-files).
The proxy reads only this configuration's referenced secrets.
It does not read Claude OAuth stores.

## 4. Start the proxy

Run these commands in the first terminal:

```sh
bin/claude-proxy --config "$proxy_config" --check
bin/claude-proxy --config "$proxy_config"
```

The first command performs local validation only.
Wait for the `ready` record from the second command.
Keep the process in the foreground.

## 5. Inspect the catalog

Repeat the profile selection in another terminal.
Then run this catalog operation:

```bash
(
  set -eu
  umask 077
  temporary=$(mktemp -d "${TMPDIR:-/tmp}/claude-proxy-catalog.XXXXXX")
  trap 'rm -rf -- "$temporary"' EXIT
  header_file="$temporary/header"
  catalog_file="$temporary/catalog"
  token=$(cat "$proxy_token_file")
  token=${token%$'\r'}
  printf 'X-Claude-Proxy-Key: %s\n' "$token" > "$header_file"
  status=$(curl --noproxy '*' --proto '=http' --connect-timeout 2 --max-time 5 \
    --fail --silent --show-error --header "@$header_file" \
    --output "$catalog_file" --write-out '%{http_code}' \
    "$proxy_base_url/v1/models?limit=1000")
  if [ "$status" != 200 ]; then
    printf 'Expected HTTP 200 from the local catalog. Stop and inspect settings.\n' >&2
    exit 1
  fi
  cat "$catalog_file"
)
```

The operation keeps the token out of curl's arguments.
It disables ambient HTTP proxies, permits only HTTP and rejects non-200 responses.
It does not follow redirects or send OAuth.
The temporary header file is removed on exit.

Compare the mixed profile's `models.*.id` with the catalog's `data.*.id`.
Make sure that every router ID exists in that catalog.
Extra catalog models do not create router routes.
Catalog success does not prove provider access or OAuth compatibility.

## 6. Start the opted-in session

Use `CLAUDE_ROUTER_PROVIDER=proxy`.
This identifier selects `proxy-router.json` inside `CLAUDE_CONFIG_DIR`.
If `CLAUDE_CONFIG_DIR` is unset, the directory is `~/.claude`.
It is not a file path.
The default provider uses `router.json`.
Unknown provider names need a complete profile and do not select the default.

CAUTION: Do not put the local token in `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN`.
Those variables can replace subscription authentication.

Use a profile without a conflicting API-key helper.
Run the session only after account and destination approval:

```bash
(
  set -eu
  headers=${ANTHROPIC_CUSTOM_HEADERS:-}
  if printf '%s\n' "$headers" |
    grep -Eiq '^[[:space:]]*(x-claude-proxy-key|authorization|x-api-key|proxy-authorization|cookie)[[:space:]]*:'
  then
    printf 'Remove conflicting reserved custom headers before starting.\n' >&2
    exit 1
  fi
  token=$(cat "$proxy_token_file")
  token=${token%$'\r'}
  unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN
  export ANTHROPIC_BASE_URL="$proxy_base_url"
  export ANTHROPIC_CUSTOM_HEADERS="${headers:+$headers$'\n'}X-Claude-Proxy-Key: $token"
  export CLAUDE_ROUTER_PROVIDER=proxy
  claude --model claude-opus-5-5
)
```

The subshell preserves unrelated custom headers and rejects reserved credential headers.
Claude Code retains responsibility for saved OAuth.
See [authentication](configuration.md#authentication) for each outbound credential mode.

Open `/router` and make sure that the selected profile and routing state are correct.
Use a configured native model ID for the initial model.
Do not use `proxy` or a router-internal alias as an API model ID.

The native model picker is not the router's inventory.
Documented discovery limits can hide non-Claude IDs or skip custom-header-only discovery.
Do not rename models or replace OAuth to bypass those limits.

## Stop and rollback

1. Exit the opted-in Claude Code session.
2. Stop the proxy with Ctrl-C or SIGTERM.
3. Start the normal client command with its original connection configuration and router profile.
4. If you exported variables elsewhere, restore their previous values first.

The subshell changes end with the session.
No global configuration, saved login or existing profile file is overwritten.
A stopped proxy causes opted-in requests to fail without fallback.

CAUTION: Rollback cannot retract history already sent or provider charges already incurred.

## Troubleshooting

| Symptom                            | Action                                                                                              |
| ---------------------------------- | --------------------------------------------------------------------------------------------------- |
| Local 401                          | Make sure that the local key matches the profile and an OAuth route receives its Bearer value       |
| Local 403                          | Use the configured Host/port and remove browser-origin headers                                      |
| Local 404                          | Use an exact configured native ID and an available path                                             |
| Local 415                          | Use JSON and an uncompressed request body                                                           |
| Router unavailable                 | Make sure that the provider identifier, configuration directory, profile and classifier are correct |
| Catalog model never selected       | Add its metadata and rules to the mixed router profile                                              |
| Upstream rejects history or effort | Exclude that provider combination until approved acceptance succeeds                                |
| Connection failure                 | Examine the endpoint, system TLS trust and network access                                           |
| Uncertain savings                  | Do not treat router estimates as provider invoices                                                  |

All local HTTP statuses are in the [reference](configuration.md#local-errors).
Development commands are in [CONTRIBUTING](../CONTRIBUTING.md#build-and-tests).
Test evidence and live gaps are in [verification](verification.md).

## Router compatibility

The profile uses the schema from
[claude-router 1.17.1, commit 7e1274b](https://github.com/alexei-led/claude-router/tree/7e1274b69969bc4ddb09d7f12e3377e6afd030e1).
The local checkout is named `claude-model-router`.
Run the existing profile validator after a schema or profile change.
This does not prove current prices, provider capabilities or actual classifier decisions.
