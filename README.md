# claude-proxy

**One local endpoint for Claude Code. Multiple native model providers.**

claude-proxy is a local Anthropic API passthrough proxy.
Keep one connection while selecting models from different providers.
Each exact model ID selects one configured upstream.

![Claude Code sends a native request through one local proxy to one selected provider.](docs/assets/overview.svg)

- **Native requests and streams.** No translation, model rewriting, proxy retries or fallback.
- **Separate credentials.** Session OAuth on explicit passthrough routes. Server-held keys for other providers.
- **Local only.** One standard-library Go binary, a loopback listener and a private local token.

Select models directly or use [claude-model-router](https://github.com/alexei-led/claude-router)
with a mixed profile. The router chooses the model and effort.
The proxy forwards the original request body and response stream.

Providers must support the native Anthropic API.
The proxy does not convert OpenAI APIs or repair incompatible conversation history.

> **Experimental:** [scoped live results](docs/verification.md#live-acceptance) cover specific client/model combinations.
> OAuth refresh, billing and untested provider combinations remain unverified.

## Install

```sh
brew install alexei-led/tap/claude-proxy
```

Homebrew is the default install method. It installs only the binary.
If Homebrew requests third-party trust, use the
[formula-specific trust step](docs/integration.md#1-install-the-proxy-and-prepare-the-client).
Configuration and startup remain manual.
[Direct binaries](https://github.com/alexei-led/claude-proxy/releases) and
[source builds](CONTRIBUTING.md#build-and-tests) remain available.

## Explore

[User guide →](docs/integration.md) ·
[Configuration reference](docs/configuration.md) ·
[Architecture](docs/design.md) ·
[Tests and readiness](docs/verification.md)

[Build and contribute](CONTRIBUTING.md) ·
[Release notes](docs/releases/v0.1.2.md) ·
[MIT license](LICENSE)
