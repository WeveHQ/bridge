# Contributing to Weve Bridge

Thanks for your interest in contributing. This document covers the process for contributing to this project.

## Reporting bugs

Open a [bug report](https://github.com/WeveHQ/bridge/issues/new?template=bug_report.yml). Include:

- Bridge version (`weve-bridge --version`)
- How you're running it (Docker, binary, source)
- Steps to reproduce
- Expected vs actual behavior
- Relevant logs (`WEVE_BRIDGE_LOG_LEVEL=debug`)

## Suggesting features

Open a [feature request](https://github.com/WeveHQ/bridge/issues/new?template=feature_request.yml). Describe the problem you're trying to solve, not just the solution you want.

## Pull requests

1. Fork the repo and create a branch from `main`.
2. Make your changes. Follow the existing code style.
3. Add or update tests for your changes.
4. Run the full test suite:
   ```bash
   go test ./...
   go vet ./...
   ```
5. Open a pull request against `main`.

Keep PRs focused — one concern per PR. If you're fixing a bug and also refactoring nearby code, split them.

### Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add timeout configuration for edge polls
fix: handle proxy auth failure gracefully
docs: clarify allow-list behavior
```

### What we look for in review

- Tests pass
- No new dependencies without discussion (this project is zero-dependency by design)
- Code is clear without excessive comments
- Security-sensitive changes get extra scrutiny

## Development setup

See [DEVELOPMENT.md](DEVELOPMENT.md) for build, test, and Docker instructions.

## License

By contributing, you agree that your contributions will be licensed under the [Apache 2.0 License](LICENSE).
