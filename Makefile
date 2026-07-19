.PHONY: dev gen check build clean

dev:
	# LiveKit must advertise an address reachable by host browsers AND the
	# egress container; use the LAN IP (falls back to loopback, host-only).
	@ip=$$(ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null || echo 127.0.0.1); \
		echo "KLISI_NODE_IP=$$ip" > deploy/.env
	docker compose -f deploy/compose.yaml up -d
	@set -e; \
		if [ -f .env ]; then set -a; . ./.env; set +a; fi; \
		(cd server && KLISI_BASE_URL=http://localhost:5173 go run ./cmd/klisi) & go_pid=$$!; \
		(cd web && npm run dev) & web_pid=$$!; \
		trap 'kill $$go_pid $$web_pid 2>/dev/null || true' INT TERM EXIT; \
		wait

gen:
	go run github.com/gzuidhof/tygo@latest generate

check:
	cd server && go vet ./...
	cd server && staticcheck ./...
	cd server && go test ./...
	cd web && npm run check
	cd web && npm run lint

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
