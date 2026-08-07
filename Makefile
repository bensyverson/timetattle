BINARY := timetattle
PKG    := ./cmd/timetattle

.PHONY: build install agent run test fmt fix vet clean profile docs docs-build help

build:
	go build -o $(BINARY) $(PKG)

# install is `go install` PLUS re-signing, and the signing is not optional
# dressing: macOS keys Full Disk Access to the binary's code signature, Go
# links ad-hoc, and a bare `go install` therefore silently revokes the FDA
# grant while System Settings still shows it enabled — that cost this project
# 44 hours of capture on 2026-07-30 and 33 hours three days earlier (see the
# Quick start in README.md). scripts/sign.sh re-signs with the stable
# identity, so the grant keyed to that identity survives every rebuild.
install:
	go install $(PKG)
	./scripts/sign.sh

# One-time (or after changing the schedule): sign AND (re)install the launchd
# agent that runs `timetattle harvest` twice a day. Re-runnable; replaces any
# existing agent. Routine reinstalls only need `make install`.
agent:
	go install $(PKG)
	./scripts/install-agent.sh

# Usage: make run ARGS="metrics -all"
run:
	go run $(PKG) $(ARGS)

test:
	go test ./...

fmt:
	go fmt ./...

fix:
	go fix ./...

vet:
	go vet ./...

clean:
	rm -f $(BINARY)

# CPU-profile a subcommand against the live store, always from a fresh build.
# Usage: make profile ARGS="metrics"
profile:
	./scripts/profile $(ARGS)

# Serve the documentation site locally on http://localhost:1313/.
# Requires `hugo` (extended). `brew install hugo` if missing.
docs:
	cd docs && hugo serve

# Build the docs site to public/.
docs-build:
	cd docs && hugo --minify

help:
	@echo "Targets:"
	@echo "  build      - compile ./$(BINARY) from $(PKG)"
	@echo "  install    - go install AND re-sign (keeps the Full Disk Access grant)"
	@echo "  agent      - install + (re)schedule the twice-daily harvest launchd agent"
	@echo "  run        - go run (pass args via ARGS=\"...\")"
	@echo "  test       - run all Go tests"
	@echo "  fmt        - go fmt ./..."
	@echo "  fix        - go fix ./..."
	@echo "  vet        - go vet ./..."
	@echo "  clean      - remove the local binary"
	@echo "  profile    - scripts/profile against a fresh build (ARGS=\"metrics\")"
	@echo "  docs       - serve docs/ on localhost:1313 (requires hugo)"
	@echo "  docs-build - build docs/ with hugo --minify"
