.PHONY: dev gen server-check web-check typesync check media media-dev moil-e2e build clean

dev:
	# LiveKit must advertise an address reachable by host browsers AND the
	# egress container; use the LAN IP (falls back to loopback, host-only).
	@ip=$$(ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null || echo 127.0.0.1); \
		echo "TIDE_NODE_IP=$$ip" > deploy/.env
	docker compose -f deploy/compose.yaml up -d
	@set -e; \
		if [ -f .env ]; then set -a; . ./.env; set +a; fi; \
		(cd server && TIDE_DEV_MODE=true TIDE_BASE_URL=http://localhost:5173 \
			TIDE_TRANSCRIPTS=$${TIDE_TRANSCRIPTS:-true} \
			TIDE_EGRESS_TEMPLATE_URL=http://host.docker.internal:5173/egress-template \
			go run ./cmd/tide) & go_pid=$$!; \
		(cd web && npm run dev) & web_pid=$$!; \
		trap 'kill $$go_pid $$web_pid 2>/dev/null || true' INT TERM EXIT; \
		wait

gen:
	go run github.com/gzuidhof/tygo@v0.2.21 generate

server-check:
	cd server && go vet ./...
	cd server && go test ./...

web-check:
	cd web && npm run check
	cd web && npm run lint

typesync: gen
	git diff --exit-code -- web/src/lib/api/types.gen.ts

check: server-check web-check typesync

# The full media gate exactly as CI runs it: builds the stack, runs every
# suite, tears down. Slow by design.
media:
	./scripts/run-media-tests.sh

# Iteration loop: keeps the stack up between runs and rebuilds only what
# changed. Pass through spec names and Playwright flags, for example:
#   make media-dev ARGS="media-lifecycle.spec.ts --project=chromium"
media-dev:
	./scripts/media-dev.sh $(ARGS)

# Transcripts end to end with the real moil binary, real uv and MinIO in
# Docker (server/e2e/README.md). Not part of check: it builds moil from a
# checkout of it. -count=1: go test's cache doesn't track the moil binary.
MOIL_REPO ?= ../moil
MOIL_BIN ?= $(abspath $(MOIL_REPO))/target/release/moil

moil-e2e:
	cd $(MOIL_REPO) && cargo build --release -p moil-cli
	cd server && MOIL_BIN=$(MOIL_BIN) go test -race -count=1 ./e2e/

build:
	cd web && npm run build
	# Go embed paths cannot cross module boundaries, so stage the SPA under server/.
	@set -e; \
		trap 'rm -rf server/web/build' EXIT; \
		rm -rf server/web/build; \
		mkdir -p server/web; \
		cp -R web/build server/web/build; \
		mkdir -p bin; \
		(cd server && go build -tags embed -o ../bin/tide ./cmd/tide)

clean:
	rm -rf bin web/build web/.svelte-kit server/web/build
