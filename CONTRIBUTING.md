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

Requirements and build steps are in the [README](./README.md). In short:

```bash
make test        # run the Go test suite
make vet         # static analysis
make fmt         # format Go code
make admin       # build the React admin console (if you touched web/admin)
```

Please keep changes focused, include tests where it makes sense, and match the
style of the surrounding code.

## License

By contributing, you agree that your contributions are licensed under the
project's license (see [LICENSE](./LICENSE) and [NOTICE](./NOTICE)).
