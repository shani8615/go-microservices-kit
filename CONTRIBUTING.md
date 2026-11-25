# Contributing to go-microservices-kit

Thanks for taking the time to help! This is a reference project, so the most valuable contributions keep it **small, correct and easy to read**.

## Reporting a bug

Open an issue using the **Bug report** template and include the output of `make logs` if a service misbehaves.

Security problems go through [private vulnerability reporting](https://github.com/shani8615/go-microservices-kit/security/advisories/new), not public issues. See [SECURITY.md](SECURITY.md).

## Development

You need Go 1.26+ and Docker.

```bash
make tidy                  # resolve dependencies
make test                  # go test -race -cover ./...
make lint                  # golangci-lint
cp .env.example .env       # fill in JWT_SECRET and POSTGRES_PASSWORD
make up && make demo       # end-to-end check
```

## Pull requests

- **One change per PR**, with tests. Every service package has in-memory stores, so most changes can be tested without Docker.
- **Prefer clarity over features.** New patterns are welcome when they are common in production (tracing, metrics, migrations); framework-style abstractions are not.
- **Never commit secrets.** Use `.env` (git-ignored) and placeholders in examples.
- Keep CI green: tests, lint and Docker builds for all four services.

## Be kind

Assume good intent, keep discussions technical, and help newcomers.
