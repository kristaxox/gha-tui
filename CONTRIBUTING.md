# Contributing

Issues and pull requests are welcome.

## Setup

You need Go 1.25 or newer. To try the tool against a real repository you also
need an authenticated [`gh` CLI](https://cli.github.com) (`gh auth login`).
The tests don't touch the network or a terminal, so they need neither.

```sh
git clone https://github.com/kristaxox/gha-tui
cd gha-tui
go run . --repo cli/cli
```

## Before you open a PR

CI runs these, so run them locally first:

```sh
gofmt -l .          # should print nothing
go vet ./...
go test -race ./...
golangci-lint run   # config in .golangci.yml
```

## Guidelines

- Keep changes small and focused; one concern per PR.
- Add or update a test for behaviour changes.
- Test fixtures and README examples use generic data (`exampleorg/widget`,
  `alice`, `bob`). Don't paste real repository names, logins or output.
- If you change a flag, update the flag table in the README.

By contributing you agree your work is released under the [MIT License](LICENSE).
