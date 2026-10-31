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

## Explore

[User guide →](docs/integration.md) ·
[Configuration reference](docs/configuration.md) ·
[Architecture](docs/design.md) ·
[Tests and readiness](docs/verification.md)

[Build and contribute](CONTRIBUTING.md) ·
[Release notes](docs/releases/v0.1.1.md) ·
[MIT license](LICENSE)
