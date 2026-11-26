# One Makefile, and CI runs what a reviewer runs: make verify.
SHELL := bash

GO      ?= go
COMPOSE := docker compose -f deploy/compose/docker-compose.yml
VERSION ?= $(shell git describe --always --dirty 2>/dev/null || echo dev)

SERVICES    := gateway auth wallet transfer notification webhook recon
DB_SERVICES := auth wallet transfer notification webhook recon
RUN_DIR     := .run

.PHONY: all build fmt fmt-check lint test test-race test-integration verify \
        infra migrate up down reset logs clean

all: build

## Build every service binary into bin/, stamped with the git version.
build:
	@mkdir -p bin
	@for s in $(SERVICES); do \
		echo "building $$s"; \
		$(GO) build -ldflags "-X main.version=$(VERSION)" -o bin/$$s ./cmd/$$s || exit 1; \
	done

fmt:
	@gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

fmt-check:
	@out=$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*')); \
	if [ -n "$$out" ]; then echo "gofmt required for:"; echo "$$out"; exit 1; fi

## vet, staticcheck, and the import-boundary test.
lint:
	$(GO) vet ./...
	$(GO) tool staticcheck ./...

## Unit and property tests. No Docker, no network.
test:
	$(GO) test -count=1 ./...

test-race:
	$(GO) test -race -count=1 ./...

## Integration tests: real PostgreSQL, NATS and Redis in throwaway containers.
test-integration:
	$(GO) test -tags=integration -count=1 -p 2 ./...

verify: fmt-check lint test-race build

## Local infrastructure: PostgreSQL, NATS with JetStream, Redis.
infra:
	$(COMPOSE) up -d --wait postgres nats redis

## Apply every service's embedded migrations to the local databases.
migrate: build
	@for s in $(DB_SERVICES); do \
		url="postgres://echo_$${s}_app:echo_$${s}@localhost:5432/echo_$${s}?sslmode=disable"; \
		echo "migrating $$s"; \
		env "$$(echo $$s | tr '[:lower:]' '[:upper:]')_DATABASE_URL=$$url" ./bin/$$s -migrate || exit 1; \
	done

## The whole platform on localhost: stack, binaries, migrations, services.
up: infra build migrate
	@mkdir -p $(RUN_DIR)
	@for s in $(SERVICES); do \
		if [ -f $(RUN_DIR)/$$s.pid ] && kill -0 "$$(cat $(RUN_DIR)/$$s.pid)" 2>/dev/null; then \
			echo "$$s already running"; continue; \
		fi; \
		url="postgres://echo_$${s}_app:echo_$${s}@localhost:5432/echo_$${s}?sslmode=disable"; \
		nohup env "$$(echo $$s | tr '[:lower:]' '[:upper:]')_DATABASE_URL=$$url" \
			./bin/$$s >>$(RUN_DIR)/$$s.log 2>&1 & \
		echo $$! > $(RUN_DIR)/$$s.pid; \
		echo "$$s started (log: $(RUN_DIR)/$$s.log)"; \
	done
	@echo "health: http://localhost:8080/healthz   metrics: http://localhost:9080/metrics"

## Stop the services we started, then the stack. Data is kept.
down:
	@for s in $(SERVICES); do \
		if [ -f $(RUN_DIR)/$$s.pid ]; then \
			kill "$$(cat $(RUN_DIR)/$$s.pid)" 2>/dev/null || true; \
			rm -f $(RUN_DIR)/$$s.pid; \
		fi; \
	done
	@$(COMPOSE) down

## Stop everything, delete the volumes, and start again. The init script only
## runs against an empty volume, so this is how a fresh database is built.
reset:
	@$(MAKE) down
	@$(COMPOSE) down -v
	@$(MAKE) up

logs:
	@tail -f $(RUN_DIR)/*.log

clean:
	@rm -rf bin $(RUN_DIR)
