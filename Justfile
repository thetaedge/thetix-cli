# Build and check the thetix CLI. `just` lists the recipes.

# The version stamped into the binary: the nearest tag plus commits since, or
# the commit, with -dirty when the tree has changes (v0.1.0-3-gabc1234-dirty).
version := `git describe --tags --always --dirty 2>/dev/null || echo dev`
ldflags := "-s -w -X main.version=" + version
bin := env_var_or_default("THETIX_INSTALL_DIR", env_var("HOME") / ".local/bin")

[private]
default:
    @just --list

# Build ./thetix with the version stamped in
build:
    go build -ldflags "{{ldflags}}" -o thetix .

# Build and copy the binary to THETIX_INSTALL_DIR (default ~/.local/bin)
install: build
    mkdir -p "{{bin}}"
    install -m 0755 thetix "{{bin}}/thetix"
    @echo "Installed {{bin}}/thetix ({{version}})"

# Run the tests
test:
    go test ./...

# Format check, vet and tests, as CI runs them
check:
    test -z "$(gofmt -l .)" || (gofmt -l . && exit 1)
    go vet ./...
    go test -race ./...

# Format the code
fmt:
    gofmt -w .

# Build every release target locally into dist/, without publishing (needs goreleaser)
snapshot:
    goreleaser release --snapshot --clean

# Remove build output
clean:
    rm -rf thetix thetix.exe dist
