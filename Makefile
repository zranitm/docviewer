# Targets the README documents: up, down, test, cover, lint.
GO        ?= go
COVER_MIN ?= 80
COMPOSE   ?= docker compose -f deploy/docker-compose.yml

.PHONY: up down test cover lint

up:            ## build and start the service, both mocks and the Grafana stack
	$(COMPOSE) up --build -d

down:          ## stop everything
	$(COMPOSE) down -v

test:          ## every test, with the race detector; no Docker needed
	$(GO) test -race ./...

# Coverage counts statements in internal/ and pkg/ only, from every test in the
# module (end-to-end tests under test/ included). cmd/ is wiring, exercised end
# to end, and stringer output (*_string.go) is generated, so both are excluded.
cover:         ## coverage report; fails below COVER_MIN
	$(GO) test -race -coverpkg=./internal/...,./pkg/... -coverprofile=coverage.out ./...
	@grep -v '_string.go' coverage.out > coverage.tmp && mv coverage.tmp coverage.out
	@$(GO) tool cover -func=coverage.out | tail -1
	@total=$$($(GO) tool cover -func=coverage.out | tail -1 | awk '{print $$3}' | tr -d '%'); \
	awk -v t="$$total" -v m="$(COVER_MIN)" 'BEGIN { if (t+0 < m+0) { printf "coverage %.1f%% is below the %d%% floor\n", t, m; exit 1 } printf "coverage %.1f%% meets the %d%% floor\n", t, m }'

lint:          ## golangci-lint, including constant log messages (sloglint)
	golangci-lint run ./...
