# Contributing to AlertLoop

Thanks for your interest in AlertLoop. This document explains how to report
issues and submit changes, and the sign-off we require on contributions.

## Issues and discussion first

Bug reports and feature ideas are very welcome — please open a GitHub issue.

For code changes, **open an issue (or discussion) before starting a pull
request**, especially for anything non-trivial. AlertLoop is an open-core
product with a deliberately scoped Community edition, so some ideas belong in
paid editions or would change product direction. Agreeing on the approach first
saves everyone time.

## Developer Certificate of Origin (DCO)

To keep the project's provenance clean, every commit must be **signed off**
under the [Developer Certificate of Origin](./DCO) (DCO 1.1). The sign-off is a
line at the end of each commit message certifying that you wrote the code (or
otherwise have the right to submit it) under the project's license:

```
Signed-off-by: Jane Doe <jane@example.com>
```

Add it automatically with the `-s` flag:

```bash
git commit -s -m "Your commit message"
```

The name and email must be your real ones and match your Git identity
(`git config user.name` / `user.email`). Pull requests whose commits are not
signed off will not be merged. A CI check enforces this.

## Development

Go 1.25 or newer; Node for the admin console. There is no CGO dependency.

```bash
make build       # build bin/alertloop with the admin console currently in internal/adminui/dist
ALERTLOOP_ADMIN_TOKEN=dev-token make run   # all-in-one on :8080, local SQLite, alertloop.example.yaml
make test        # run the Go test suite (PostgreSQL tests skip without a DSN)
make vet         # static analysis
make fmt         # format Go code
```

### Admin console

The React console lives in `web/admin`; the Go build embeds
`internal/adminui/dist`.

```bash
cd web/admin
npm install
npm run dev      # http://localhost:5273, proxies /v1 to localhost:8080
```

`make admin` builds it into `internal/adminui/dist`. The Docker image build does
this itself.

### Release builds

Pushing a version tag runs `.github/workflows/release.yml`: it builds the admin
console, cross-compiles every target, and publishes the binaries with a signed
`checksums_*.txt` to the GitHub Release. On the tag, `ci.yml` and `release.yml`
run in parallel, so the slow CI jobs (`upgrade`, `compose-up`, and `image`, the
amd64 and arm64 image build) must pass before the tag: push the release commit
to a `release/**` branch, or run CI by hand (Actions → CI → Run workflow). To
build the same artifacts locally:

```bash
make admin
make release     # or: VERSION=v0.1.0 ./scripts/build-release.sh; output in dist/
```

Please keep changes focused, include tests where it makes sense, and match the
style of the surrounding code.

## License

By contributing, you agree that your contributions are licensed under the
project's license (see [LICENSE](./LICENSE) and [NOTICE](./NOTICE)).
