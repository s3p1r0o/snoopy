# Changelog

All notable changes to this project are documented here.
Format based on [Keep a Changelog](https://keepachangelog.com/); the project does not yet follow SemVer (single-binary tool, version stamped at build time).

## [Unreleased]

- **npm page.** README opens with install, usage and environment for package users (development notes follow); `package.json` gains repository, homepage, bugs, keywords and author; MIT `LICENSE` file added (shipped in the package).
- **Docs.** `npm publish ./dist/*.tgz`: without the `./`, npm reads `dist/<file>` as a GitHub repository.

## [0.1.0] - 2026-10-10

- **Releases.** Tags are `v<version>`; pushing one runs the `release` workflow, which publishes the four proxy tarballs (linux/darwin × amd64/arm64) and `SHA256SUMS` as the GitHub Release the npm launcher downloads from. Asset names and the stamped version drop the `v`.
- **npm package.** Publishable (no longer `private`, public scoped access). `just npm-release` takes the package version from the release tag, refuses a dirty or untagged HEAD, and leaves `package.json` unchanged.
- **Launcher.** An existing dsh profile is retargeted at each launch's proxy port; previously the first boot's port stayed in place and dsh lost the proxy when the port changed.
- **Dev harness.** `npm run harness` builds the proxy from `proxy/` (it ran `go build` at the repo root and ignored the failure).
- **Docker.** The Go stage cross-compiles on the build host; `just docker-buildx` builds linux/amd64 + linux/arm64 images.
- **CI.** Cross-compiles the release targets on every push.
