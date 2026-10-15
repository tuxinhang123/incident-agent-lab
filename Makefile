GO ?= go
GOFLAGS_TEST := -p 1
COMPOSE := docker compose -f deploy/compose.yaml

.PHONY: build test vet tidy up down fault-inject fault-recover local-up local-down logs verify eval clean

build:
	$(GO) build -o bin/demo-api ./cmd/demo-api
	$(GO) build -o bin/agent ./cmd/agent

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

# Day 5 offline eval suite: 5 cases with automatic env prep, fault
# injection and a JSON report under .run/evals/.
eval:
	go run ./cmd/eval

clean:
	rm -rf bin .run
