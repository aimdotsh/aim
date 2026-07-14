.PHONY: build web test clean

build: web
	go build -o dist/aim-console ./cmd/aim-console
	go build -o dist/aim-executor ./cmd/aim-executor

web:
	cd web && npm ci --no-audit --no-fund && npm run build

test:
	bash -n aim.sh tests/*.sh scripts/*.sh
	for test_script in tests/*.sh; do bash "$$test_script"; done
	go test ./...
	go vet ./...
	cd web && npm ci --no-audit --no-fund && npm run build

clean:
	go clean
	rm -rf dist web/node_modules
