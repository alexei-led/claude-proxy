# Managed proxy lifecycle

## Approved design

Shared managed brokers per canonical configuration and generation, separate for personal and work. `claude-proxy run --config PATH -- COMMAND` starts or reuses the matching broker, registers the current PID before exec, and keeps terminal/exit behavior unchanged. `connect` and manually started servers remain available.

- Default managed idle timeout: 5m; `0` disables auto-close.
- Close only after zero living registered clients and zero active forwarded requests for the idle window.
- macOS uses kqueue process-exit watches; Linux checks process birth identity and exit state. Uncertain tracking keeps the broker alive.
- No heartbeats or traffic-only inactivity detection. Detached clients must register separately.
- Canonical config path, immutable config/credential snapshot and installed executable identify a generation. New launches use a new generation after upgrades; old sessions keep their original broker until exit.
- Private owned runtime directory, bounded control messages and logs, startup locking, readiness handshake, no credentials in arguments/logs/control replies.
- `runtime.max_inflight`: default 8, bounded configurable limit. Test 32 before setting it in personal/work examples and chezmoi.
- No provider retry/fallback/translation, login service, automatic package upgrade or cross-profile credential pooling.

## Execution

- [x] Add strict optional runtime settings with backward-compatible defaults and tests.
- [x] Propagate configurable concurrency through admission and transport; serialize idle drain with request admission.
- [x] Add generation identity, private runtime paths and safe startup locks.
- [x] Add process-backed registration for Darwin/Linux and conservative uncertain-liveness behavior.
- [x] Add detached broker startup, bounded control/status protocol and readiness checks.
- [x] Add managed run command preserving exec/PID/signals and integrate with existing environment checks.
- [x] Test shared startup, config/generation separation, process exit/crash, idle races, active streams, capacity and unsafe paths.
- [x] Run fake-provider concurrency checks at 1 and 32 with bounded bodies.
- [x] Update user/configuration/architecture/verification documentation.
- [x] Prepare chezmoi source launchers for managed run and runtime settings for both accounts; apply only after a compatible binary is installed.
- [ ] Run relevant proxy and chezmoi checks, independent review and installed-path/synthetic acceptance.
- [ ] Commit and create PR; publication follows explicit repository/tag approval. Apply managed launchers/settings only with a compatible installed binary.

## Boundaries

This replaces the manual-only startup constraint with explicitly invoked launcher-managed startup. It does not start proxies at login. Existing native protocol and saved OAuth ownership are unchanged. Broker runtime files and tokens are machine state, not committed configuration. Other active worktrees and unrelated changes are outside scope.
