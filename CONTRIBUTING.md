# Contributing

## Prerequisites

- Go 1.26+
- [`golangci-lint`](https://golangci-lint.run/welcome/install/) (for `make lint`)
- [`govulncheck`](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) (for `make vuln`)

## Dev workflow

```bash
git clone https://github.com/riftwerx/company-research
cd company-research

make test          # run unit tests
make lint          # run linter
make vuln          # check for known vulnerabilities
make local-release # install binary to $GOPATH/bin with version injected
```

> `make` targets require a Unix-like environment. Windows users without WSL can run
> `go test ./...`, `golangci-lint run ./...`, and `govulncheck ./...` directly.

## Making changes

- Branch off `main`; keep PRs focused on a single concern
- All three CI jobs (`test`, `lint`, `vuln`) must pass before merging

## Commit messages

Use conventional prefixes — goreleaser uses these to build the changelog:

| Prefix | Use for | Appears in changelog? |
|---|---|---|
| `feat:` | new functionality | yes |
| `fix:` | bug fixes | yes |
| `chore:` | maintenance, deps | no |
| `docs:` | documentation only | no |
| `test:` | test-only changes | no |

## Coding conventions

**Errors**
- Wrap with `%w` so callers can use `errors.Is`
- Sentinel errors: `var ErrXxx = errors.New(...)` in the owning package

**Testing**
- Arrange / Act / Assert structure with blank-line separation
- `t.Parallel()` at the top of every test
- Subtests via `t.Run`; names prefixed with `should`
- Table-driven tests use `test` as the loop variable

**Context**
- Always the first parameter; never stored in a struct
- `context.Background()` only at entry points (`main`, test setup)

**Interfaces**
- Define in the consuming package, not the implementing one
- Keep them small; don't define until there is a concrete need

**Architecture**
- Dependency flow: `main → mcp → companyhouse → client`
- Inject config via `New()`; call `os.Getenv` only in `main.go`

## Reporting issues

Open a [GitHub Issue](https://github.com/riftwerx/company-research/issues).
