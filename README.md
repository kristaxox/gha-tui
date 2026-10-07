# gha-tui

[![CI](https://github.com/kristaxox/gha-tui/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/kristaxox/gha-tui/actions/workflows/ci.yml)

A single dependency-free Go binary that draws a GitHub repository's open pull
requests and the GitHub Actions checks running on them as a live, navigable
tree in the terminal.

```
 exampleorg/widget                                4 open · 1 failing · 1 in flight
 ├─ ✓ main a1b2c3d  Merge pull request #334       7/7 · 2h ago
 ├─ ✗ #336 Boot the simulator before ios-test     alice · 5/7 · 12m ago
 │  ├─ ✗ iOS Field                                3/4 4m12s
 │  │  ├─ ✓ ios-build                             1m50s
 │  │  └─ ✗ ios-test                              4m12s
 │  └─ ✓ CI (3)
 └─ ⠙ #337 Record CI Mac versions                 bob · 4/7 · just now
```

The tree has four levels: repository → pull request → workflow → check run. A
parent shows the worst state among its children, and failure deliberately
outranks running, so a red pull request stays red while a retried job spins.

The repository's default branch sits above the pull requests with its own
checks, so a red pull request can be read against the question it always
raises: is main itself broken, or is this change? Pass `--no-main` (or press
`m`) to leave it out.

## Install

```sh
go install github.com/kristaxox/gha-tui@latest
```

## Prerequisites

`gha-tui` shells out to the [`gh` CLI](https://cli.github.com), which owns
authentication, host selection and rate limiting. Authenticate once:

```sh
gh auth login
```

## Use

```sh
gha-tui                               # the current checkout's repository
gha-tui --repo cli/cli                # any repository
gha-tui --once                        # print the tree and exit
gha-tui --interval 10s                # refresh more often
```

Run inside a GitHub checkout and it watches that repository; outside one,
pass `--repo OWNER/NAME`.

### Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--repo OWNER/NAME` | the current checkout | repository to watch |
| `--interval` | `30s` | auto-refresh interval; `0` disables auto-refresh |
| `--limit` | `30` | maximum open pull requests to fetch |
| `--once` | off | print the tree once, without cursor or chrome, and exit |
| `--no-color` | off | disable colour (`NO_COLOR` is honoured too) |
| `--ascii` | off | ASCII glyphs instead of box drawing and braille |
| `--no-main` | off | omit the default branch's own checks from the tree |

### Keys

| Key | Action |
| --- | --- |
| `j` / `k`, `↓` / `↑` | move the cursor |
| `J` / `K` | jump to the next / previous pull request |
| `h` / `l`, `←` / `→` | fold, unfold (`h` on a folded node goes to its parent) |
| `space`, `enter`, `tab` | toggle the node under the cursor |
| `e` / `c` | unfold everything / fold everything |
| `g` / `G` | first / last row |
| `ctrl-d` / `ctrl-u` | page down / up |
| `f` | cycle filter: all → needs attention → in flight → mine |
| `d` | show or hide draft pull requests |
| `m` | show or hide the default branch |
| `o` | open the selected check or pull request in a browser |
| `r` | refresh now |
| `?` | help |
| `q`, `esc`, `ctrl-c` | quit |

## Folding

The default fold state is computed rather than stored: the repository and its
pull requests are open, and a workflow opens only when it is failing, cancelled
or running. A green workflow that starts failing therefore unfolds itself on the
next refresh, while a fold you chose yourself survives one.

A refresh never moves the cursor, and a failed refresh keeps the last good tree
on screen with the error on the status line.

## Scripting

`--once` prints the tree without cursor, colour or chrome, for scripts and
agents:

```console
$ gha-tui --once --repo exampleorg/widget
exampleorg/widget                                                      4 open · 1 failing
├─ ✓ main a1b2c3d  Merge pull request #334 from exampleorg/cache          7/7 · 2h ago
├─ ✗ #336 Boot the simulator before ios-test    alice · 5/7 · review required · 12m ago
│  ├─ ✗ iOS Field                                                              3/4 4m12s
│  │  ├─ ✓ ios-build                                                               1m50s
│  │  └─ ✗ ios-test                                                                4m12s
│  └─ ✓ CI (3)                                                                 3/3 2m05s
└─ ✓ #337 Record CI Mac versions                          bob · 7/7 · approved · 1h ago
```

`NO_COLOR` and `--ascii` cover terminals without colour, box drawing or the
braille spinner.

## Development

```sh
go build ./...
go test ./...          # no network and no TTY required
go vet ./...
golangci-lint run
```

The default branch and every open pull request come back in one GraphQL round
trip, sharing a single rollup fragment.

The pure logic — the state rollup, the tree, key handling and rendering — lives
in `internal/ghatui` as functions over an injected `now time.Time`; `main.go`,
`terminal.go`, `keys.go` and `loop.go` own the terminal, the tickers and the
browser launch. That split is what lets the tests run without a network or a
terminal.

## License

[MIT](LICENSE)
