.PHONY: build web test clean host-kit

build: web
	go build -o dist/aim-console ./cmd/aim-console
	go build -o dist/aim-executor ./cmd/aim-executor

web:
	cd web && npm ci --no-audit --no-fund && npm run build

test:
	bash -n aim.sh router.sh tests/*.sh scripts/*.sh
	for test_script in tests/*.sh; do bash "$$test_script"; done
	go test ./...
	go vet ./...
	cd web && npm ci --no-audit --no-fund && npm run build

host-kit:
	mkdir -p dist/host-kit
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/host-kit/aim-executor-linux-amd64 ./cmd/aim-executor
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/host-kit/aim-executor-linux-arm64 ./cmd/aim-executor
	chmod 0755 dist/host-kit/aim-executor-linux-amd64 dist/host-kit/aim-executor-linux-arm64 router.sh scripts/aim-copy-id scripts/install-staged-target.sh

clean:
	go clean
	rm -rf dist web/node_modules
