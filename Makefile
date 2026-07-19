.PHONY: dev gen server-check web-check typesync check build clean

dev:
	# LiveKit must advertise an address reachable by host browsers AND the
	# egress container; use the LAN IP (falls back to loopback, host-only).
	@ip=$$(ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null || echo 127.0.0.1); \
		echo "KLISI_NODE_IP=$$ip" > deploy/.env
	docker compose -f deploy/compose.yaml up -d
	@set -e; \
		if [ -f .env ]; then set -a; . ./.env; set +a; fi; \
		(cd server && KLISI_DEV_MODE=true KLISI_BASE_URL=http://localhost:5173 go run ./cmd/klisi) & go_pid=$$!; \
		(cd web && npm run dev) & web_pid=$$!; \
		trap 'kill $$go_pid $$web_pid 2>/dev/null || true' INT TERM EXIT; \
		wait

gen:
	go run github.com/gzuidhof/tygo@latest generate

server-check:
	cd server && go vet ./...
	cd server && go test ./...

web-check:
	cd web && npm run check
	cd web && npm run lint

typesync: gen
	git diff --exit-code -- web/src/lib/api/types.gen.ts

check: server-check web-check typesync

build:
	cd web && npm run build
	# Go embed paths cannot cross module boundaries, so stage the SPA under server/.
	@set -e; \
		trap 'rm -rf server/web/build' EXIT; \
		rm -rf server/web/build; \
		mkdir -p server/web; \
		cp -R web/build server/web/build; \
		mkdir -p bin; \
		(cd server && go build -tags embed -o ../bin/klisi ./cmd/klisi)

clean:
	rm -rf bin web/build web/.svelte-kit server/web/build
