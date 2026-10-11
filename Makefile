GO ?= go
GOFLAGS_TEST := -p 1
COMPOSE := docker compose -f deploy/compose.yaml

.PHONY: build test vet tidy up down fault-inject fault-recover local-up local-down logs verify clean

build:
	$(GO) build -o bin/demo-api ./cmd/demo-api

test:
	$(GO) test $(GOFLAGS_TEST) ./...

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

up:
	$(COMPOSE) up -d --build

down:
	$(COMPOSE) down

fault-inject:
	$(COMPOSE) stop redis

fault-recover:
	$(COMPOSE) start redis

logs:
	$(COMPOSE) logs -f demo-api

local-up: build
	bash scripts/local-up.sh

local-down:
	bash scripts/local-down.sh

verify:
	bash scripts/verify.sh

clean:
	rm -rf bin .run
