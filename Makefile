BINARY  := tunetty
MAIN    := ./cmd/tunetty
BIN     := bin

# svu runs git with versionsort.suffix forced. If the global gitconfig also sets
# the deprecated versionsort.prereleasesuffix, git prints a warning that svu
# parses as a version and dies on — but only once the repo has two or more tags,
# so it looks fine right up until the second release. Reading tags needs nothing
# from the global config, so it is neutralised for svu alone; the `git tag`
# below still sees the user's identity.
SVU := GIT_CONFIG_GLOBAL=/dev/null svu

VERSION ?= $(shell $(SVU) current 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo none)
DATE    ?= $(shell git log -1 --format=%cI 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

export CGO_ENABLED = 0

.PHONY: all build install test lint fmt tidy version snapshot clean \
	release-patch release-minor release-major

all: build

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/$(BINARY) $(MAIN)

# install puts the binary into $GOBIN (default ~/go/bin), with the version
# information a plain `go install` cannot inject.
install:
	go install -trimpath -ldflags '$(LDFLAGS)' $(MAIN)

test:
	go test ./...

lint:
	golangci-lint run ./...

tidy:
	go mod tidy

version:
	@$(SVU) current

snapshot:
	goreleaser release --snapshot --clean

release-patch: BUMP := patch
release-minor: BUMP := minor
release-major: BUMP := major

# .svu.yml keeps `svu next` inside 0.x, but `svu major` is explicit and ignores
# that setting, so leaving 0.x needs ALLOW_V1=1 to be stated on the spot.
release-patch release-minor release-major:
	@cur=$$($(SVU) current); \
	if [ "$(BUMP)" = "major" ] && [ "$${cur#v0.}" != "$$cur" ] && [ "$(ALLOW_V1)" != "1" ]; then \
		echo "refusing to promote $$cur to v1.0.0."; \
		echo "0.x makes no stability promise, so breaking changes belong in a minor bump:"; \
		echo "    make release-minor"; \
		echo "if 1.0.0 really is intended, state it explicitly:"; \
		echo "    make release-major ALLOW_V1=1"; \
		exit 1; \
	fi; \
	v=$$($(SVU) $(BUMP)) && \
	git tag -a "$$v" -m "$$v" && \
	echo "tagged $$v — push with: git push origin $$v"

clean:
	rm -rf $(BIN) dist
