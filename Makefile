BINARY := timetattle
PKG    := ./cmd/timetattle

.PHONY: build install agent run test fmt fix vet clean profile gate classify docs docs-build help

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
# agent that runs `timetattle harvest` every four hours. Re-runnable; replaces any
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

# THE PUBLICATION GATE: the whole tree, plus every object in .git, judged
# against a list derived from this machine's registry, config and keychain.
#
# This is a different question from the one the pre-commit hook asks. The hook
# asks "does this commit ADD anything unpublishable", measured against the same
# files at HEAD, so that a backlog of existing findings cannot block unrelated
# work and get the hook switched off. This asks "is this repository publishable
# AS IT STANDS", which is the only question that matters before pushing to a
# public mirror -- and it includes the objects a working tree cannot show you,
# because an amended-away commit is still in .git/objects long after the file
# looks clean.
#
# Exit 1 means findings. Exit 2 means the list could not be built, which is NOT
# a pass: see docs/content/docs/confidentiality.md.
#
# Built rather than `go run`: `go run` exits 1 for any non-zero program exit,
# which would collapse "found something" (1) into "could not tell" (2).
gate:
	@bin="$$(mktemp -d)/confidential"; \
	go build -buildvcs=false -o "$$bin" ./scripts/confidential && \
	"$$bin" -dir . -git -gate -v; \
	status=$$?; rm -rf "$$(dirname "$$bin")"; exit $$status

# Print the classification file for a human to read and save. Every registered
# name in it is one this machine knows about and the tree contains; saving it
# says each is safe to publish. A client's name is not, and belongs in a
# pseudonymize run instead.
classify:
	go run ./scripts/confidential -classify

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
	@echo "  agent      - install + (re)schedule the four-hourly harvest launchd agent"
	@echo "  run        - go run (pass args via ARGS=\"...\")"
	@echo "  test       - run all Go tests"
	@echo "  fmt        - go fmt ./..."
	@echo "  fix        - go fix ./..."
	@echo "  vet        - go vet ./..."
	@echo "  clean      - remove the local binary"
	@echo "  profile    - scripts/profile against a fresh build (ARGS=\"metrics\")"
	@echo "  gate       - the publication gate: whole tree AND every git object"
	@echo "  classify   - print the classification file the gate reads (review before saving)"
	@echo "  docs       - serve docs/ on localhost:1313 (requires hugo)"
	@echo "  docs-build - build docs/ with hugo --minify"
