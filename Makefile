# Сборка: SPA собирается в internal/web/dist и встраивается в бинарь.
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/ibuben/squareball/app.Version=$(VERSION)

.PHONY: all web build test test-it e2e devstand run clean

all: web build

web:
	cd web && npm ci && npm run build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/webmail ./cmd/webmail

test:
	go vet ./...
	go test ./...
	cd web && npm run typecheck

# Интеграционные тесты против локального стенда (make devstand).
test-it:
	WEBMAIL_IT=1 go test ./internal/api/ -run Integration -v

e2e:
	cd web && node e2e/smoke.mjs

devstand:
	sudo sh deploy/devstand/setup-debian.sh

run: build
	./bin/webmail serve -config deploy/devstand/config.yaml

clean:
	rm -rf bin web/e2e/out
