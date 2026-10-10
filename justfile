# Common tasks. Run `just --list` (or bare `just`) for the full menu.
# Layout: npm package (root, @s3p1r0o/snoopy) + Go proxy (proxy/).

# Toolchain version, stamped into the binary, the Docker image and the npm
# package. Release tags are `v<version>`; the leading `v` is dropped here, so
# tag v0.1.0 gives version 0.1.0 and assets proxy-0.1.0-<os>-<arch>.tar.gz.
# Stripped down to [A-Za-z0-9._-] so a crafted tag name cannot inject sh syntax
# anywhere.
version := `v=$(git describe --tags --dirty --always 2>/dev/null); [ -n "$v" ] && echo "${v#v}" | tr -cd '[:alnum:]._-' || echo dev`

# Default recipe: show the menu.
default:
    @just --list

# Build the ./build/proxy binary with the version stamped in.
build:
    cd proxy && go build -o ../build/proxy -ldflags "-X main.version={{version}}" .

# Run gofmt, vet and staticcheck on the Go proxy (the same checks CI runs).
lint:
    @cd proxy && test -z "$(gofmt -l .)" || (echo "gofmt needed on:"; gofmt -l .; exit 1)
    cd proxy && go vet ./...
    cd proxy && go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...

# Run the full Go test suite with the race detector.
test:
    cd proxy && go test -race -count=1 ./...

# Node-side unit tests (both launchers' shared helpers).
node-test:
    npm test

# Build, lint and test: everything a commit should survive.
check: build lint test

# Start the proxy alone (pass secrets via env vars, not flags: `ps` shows them).
run *flags:
    go build -C proxy -o ../build/proxy -ldflags "-X main.version={{version}}" .
    ./build/proxy {{flags}}

# Build the images: snoopy (full kit; default target) / --target proxy.
docker-build:
    docker build --build-arg VERSION={{version}} -t snoopy:{{version}} .

# Multi-arch build through buildx (the Go stage cross-compiles natively; the
# Node stage runs under QEMU for foreign arches, so binfmt must be installed).
# A multi-platform result cannot be loaded into the classic image store: pass
# --push (with a registry image name) or an --output.
# Usage: just docker-buildx ghcr.io/<owner>/snoopy linux/amd64,linux/arm64 --push
docker-buildx image="snoopy" platforms="linux/amd64,linux/arm64" *flags:
    docker buildx build --platform {{platforms}} --build-arg VERSION={{version}} -t {{image}}:{{version}} {{flags}} .

# Smoke-test the Docker image on loopback 127.0.0.1:18081/healthz, then `docker stop`.
docker-run *flags:
    docker run --rm -d --name snoopy-smoke -p 127.0.0.1:18081:8081 snoopy:{{version}} {{flags}}

# Boot the dev harness: proxy + DeepSeek Harness (dsh), DSH_HOME=.dsh.
# Usage: just harness            (default mode: web UI)
#        just harness headless "task" | --help
harness mode="web" *args:
    npm run harness -- {{mode}} {{args}}

# Cross-compile release tarballs (linux/darwin, amd64/arm64) into build/release/,
# with a SHA256SUMS file. Each tarball carries the version-stamped binary only.
release:
    #!/usr/bin/env sh
    set -eu
    release_dir="build/release/{{version}}"
    rm -rf "build/release/{{version}}" 2>/dev/null || true
    mkdir -p "$release_dir"
    for os_arch in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64; do
        os="${os_arch%-*}"
        arch="${os_arch#*-}"
        dist="proxy-{{version}}-${os_arch}"
        CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -C proxy -trimpath \
            -ldflags "-s -w -X main.version={{version}}" \
            -o "../$release_dir/$dist/proxy" .
        tar -czf "$release_dir/$dist.tar.gz" -C "$release_dir" "$dist"
        rm -rf "$release_dir/$dist"
    done
    (cd "$release_dir" && sha256sum ./*.tar.gz > SHA256SUMS)
    ls -la "$release_dir"

# Assemble the npm package @s3p1r0o/snoopy (launcher + bundled
# profile) into dist/ as a .tgz ready for publication. The release version
# (tag v<version>) becomes the package version, which the launcher uses to
# download the matching release assets. Only a clean, tagged HEAD qualifies:
# any other version has no GitHub Release behind it. package.json is stamped
# for the pack only and restored afterwards, so the tree stays clean.
npm-release:
    #!/usr/bin/env sh
    set -eu
    case "{{version}}" in *-dirty) echo "npm-release: working tree is dirty ({{version}})" >&2; exit 1 ;; esac
    if ! git describe --tags --exact-match >/dev/null 2>&1; then
        echo "npm-release: HEAD is not a release tag (v<version>); tag and push it first" >&2
        exit 1
    fi
    cp package.json package.json.orig
    trap 'mv package.json.orig package.json' EXIT
    node -e "const fs=require('fs');const p=JSON.parse(fs.readFileSync('package.json','utf8'));p.version='{{version}}';fs.writeFileSync('package.json',JSON.stringify(p,null,2)+'\n')"
    rm -rf dist
    mkdir -p dist
    npm pack --pack-destination dist/
    sha256sum dist/*.tgz | tee dist/SHA256SUMS
    ls -la dist/