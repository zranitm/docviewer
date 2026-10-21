# docviewer

A Go practice project: a resilient read-side aggregator. It takes a vehicle
identification number (VIN), queries two upstream systems in parallel and
returns one paged list of documents, newest first, each tagged with the
system it came from. The scenario is an invented dealership: staff want every
document about a vehicle in one place, but the paperwork is split between a
Sales system and a Service system, both mocked here.

The point of the project is everything around that simple sentence:

- **Fan-out and merge** across sources with different paging contracts,
  without ever re-sorting a source's own order or losing a document between
  pages.
- **An opaque, stable cursor** that encodes where paging stands in every
  source, survives sources being added, and rejects tampering.
- **Partial-failure semantics** that a client can act on: a slow or failing
  system yields a partial page that names it, transient failures are retried
  on later pages, permanent ones end the list without that system, and a
  dead page is a clear error.
- **Resilience in the transport:** per-source timeouts, bounded retries with
  a time budget, circuit breakers with recovery, and request coalescing.
- **Observability as a feature:** OpenTelemetry traces, metrics and logs with
  PII redacted, a trace ID on every response, and a provisioned Grafana
  dashboard.
- **Tests that prove the requirements:** table-driven tests everywhere, the
  race detector always on, acceptance criteria named in the end-to-end
  tests, and a coverage gate.

## Requirements

Functional:

| ID | Requirement |
|----|-------------|
| R-01 | Search by VIN: 17 characters, no I, O or Q; case and surrounding spaces are normalised; anything else is rejected with a reason |
| R-02 | One list from both systems, newest first; ties broken by source name, then by each source's own order |
| R-03 | Every document names its system, carries a canonical type, a title, an optional summary, an issue date and a link that opens the file in the source system |
| R-04 | Paged with an opaque cursor: 20 documents by default, up to 100; `next_cursor` is null on the last page; nothing is skipped or repeated across pages |
| R-05 | If one system is slow or down, the page shows the others and names the failed one; a transient failure is retried on later pages, a permanent one ends the list without that system |
| R-06 | If no system answers and a retry could help, the API returns an error that says so; once documents have been shown and every remaining system has failed permanently, the list simply ends |
| R-07 | Adding a source system means a client, an adapter, one registry line and one config entry; no existing code changes |

Non-functional:

| ID | Requirement |
|----|-------------|
| N-01 | 2 seconds per system, 3 seconds per request, whatever the systems do |
| N-02 | Stateless: any instance can serve any page; no database |
| N-03 | Repeated failures to one system open a circuit breaker that fails fast and probes after a cooldown; retries only for idempotent requests and only within the time budget |
| N-04 | Traces, metrics and logs over OTLP; VINs and document fields never appear in them; every response carries the trace ID that finds it |
| N-05 | Configuration from YAML with environment overrides, validated at start; no secrets in the repository |
| N-06 | At least 80% statement coverage in `internal/` and `pkg/`, every test under the race detector, no test logic in production code |

The requirements are written up in full, with assumptions and acceptance
criteria, in the design documents (see [Design](#design)).

## Architecture at a glance

- **A stateless Go service** (Gin): every page is composed live from the
  systems of record, so any instance can serve any page. No database, on
  purpose (HLD section 8).
- **One adapter per system** turns its HTTP client into a pageable source;
  each client's transport carries a circuit breaker and a bounded retry.
- **An aggregator** fans out to every source, merges newest first, cuts the
  page, and encodes where each source stands in the cursor. Identical
  concurrent requests share one fan-out.
- **Observability:** OpenTelemetry traces, metrics and logs over OTLP to
  `grafana/otel-lgtm`, with PII (VINs, paths, document fields) redacted
  before anything is logged or traced.
- **Built to extend:** a new source system is a client, an adapter, one
  registry line and one config entry; no existing code changes (HLD
  section 15).

## Prerequisites

- Docker with Compose
- Go 1.27 or newer, to run tests outside Docker (a C compiler is needed for
  the race detector; on Windows, for example `winget install BrechtSanders.WinLibs.POSIX.UCRT`)
- make
- golangci-lint v2, for `make lint`

## Build and run

```bash
make up     # build and start the service, both mocks, the Grafana stack and the demo UI
make down   # stop everything
```

| What | URL |
|------|-----|
| Document API | `http://localhost:8080` |
| Demo UI | `http://localhost:5173` |
| Mock Sales System API | `http://localhost:8081` |
| Mock Service System API | `http://localhost:8082` |
| Grafana (opens on the "docviewer" dashboard: request rate and latency, per-source results and latency, breaker state, coalescing, logs; Explore for Tempo traces) | `http://localhost:3000` |

Health checks: `GET /healthz` (liveness) and `GET /readyz` (readiness).

To run outside Docker, start the three binaries from the repository root
(the mocks listen on :8081 and :8082; the service needs to be told where
they are):

```bash
go run ./cmd/mock-sales &
go run ./cmd/mock-service &
DOCVIEWER_SOURCES_UPSTREAMS='{"sales":{"base_url":"http://localhost:8081","timeout":"2s"},"service":{"base_url":"http://localhost:8082","timeout":"2s"}}' go run ./cmd/docviewer
```

Without a collector listening, telemetry export failures are logged at debug
level and nothing else changes.

## Configuration

- **Defaults** live in `configs/config.yaml`. For local changes, add a
  git-ignored `configs/config.local.yaml`, or point `CONFIG_PATH` at another
  file.
- **Overrides:** any key can be set from the environment as `DOCVIEWER_`
  plus its path in upper case, for example `DOCVIEWER_SERVER_ADDR=:9090` or
  `DOCVIEWER_SOURCES_REQUEST_TIMEOUT=5s`. The upstreams map is set as a whole,
  as JSON (`DOCVIEWER_SOURCES_UPSTREAMS`).
- **`.env`:** copy `.env.example` to `.env` (git-ignored) and uncomment what
  you need; it lists every override. Precedence, lowest to highest: the YAML
  file, `.env`, real environment variables.
- **Validated at start:** a misspelt key or an invalid value stops the
  service with a message naming the key.
- **Worth knowing:** `server.cors_origins` (empty by default: no browser
  origin may call the API; docker-compose allows `http://localhost:5173` for
  the demo UI), `telemetry.redact.enabled` (true by default; off logs a
  warning at start), and the standard `OTEL_EXPORTER_OTLP_ENDPOINT`.

The mocks have their own small files, `configs/mock-sales.yaml` and
`configs/mock-service.yaml`, with prefixes `MOCK_SALES_` and `MOCK_SERVICE_`.

## Test

```bash
make test   # every test, with -race; no Docker needed
make cover  # coverage report; fails below 80%
make lint   # golangci-lint, including constant log messages
```

- **Table-driven throughout:** each test is a table of named cases.
- **Acceptance tests:** every acceptance criterion from the PRD (AC-01 to
  AC-12) is a row in an end-to-end table, named by its ID, in
  `test/e2e_test.go` (`go test ./test/ -run 'TestAcceptanceCriteria/AC-05'`
  runs one). Further end-to-end rows cover the circuit breaker opening and
  recovering, the terminal page after permanent failures, and the contract
  details.
- **Unit and end-to-end tests:** unit tests per package with fakes, and
  end-to-end tests of the service built as in production against both mocks,
  in process.
- **Coverage:** at least 80% of statements in `internal/` and `pkg/`; `cmd/`
  (wiring, covered end to end) and generated code are excluded. The current
  figure is around 95%.
- **No test logic in production code:** no test-only flags, branches or
  exported helpers. Tests use the seams production already has (interfaces,
  constructors, `httptest`), same-package `_test.go` files, and
  `testing/synctest` for time.

## Try it

`GET /v1/vehicles/{vin}/documents?limit=...&cursor=...`, documented in
`api/openapi.yaml`.

Fixture VINs (the mocks' data):

| VIN | Sales | Service |
|-----|-------|---------|
| `1M8GDM9AXKP042788` | 5 documents | 5 records (two on the same timestamp) |
| `WVWZZZ1JZXW000002` | 3 documents | nothing (404) |
| `JH4KA7561PC000003` | nothing (404) | 4 records |
| `5YJ3E1EA7KF000004` | 8 documents | 120 records over many pages, with same-timestamp ties |
| `1HGCM82633A000005` | nothing | nothing |

```bash
VIN=1M8GDM9AXKP042788
curl -s "localhost:8080/v1/vehicles/$VIN/documents" | jq .

# Page through: pass next_cursor back until it is null
curl -s "localhost:8080/v1/vehicles/$VIN/documents?limit=2" | jq '{documents, next_cursor}'
NEXT=$(curl -s "localhost:8080/v1/vehicles/$VIN/documents?limit=2" | jq -r .next_cursor)
curl -s "localhost:8080/v1/vehicles/$VIN/documents?limit=2&cursor=$NEXT" | jq .

# Make the Service system hang (a transient failure), search again, then reset
curl -s -X POST localhost:8082/admin/faults -d '{"hang": true}'
curl -s "localhost:8080/v1/vehicles/$VIN/documents" | jq .sources
curl -s -X DELETE localhost:8082/admin/faults

# Make it reject every call (a permanent failure): the list ends without it
curl -s -X POST localhost:8082/admin/faults -d '{"status": 403}'
curl -s "localhost:8080/v1/vehicles/5YJ3E1EA7KF000004/documents?limit=3" | jq .sources
curl -s -X DELETE localhost:8082/admin/faults

# Other faults: {"latency_ms": 2500}, {"error_rate": 0.5}; GET /admin/faults shows the one in force
```

Open any `url` from the response in a browser to get the document's
placeholder PDF. Traces for each search are in Grafana (Explore, Tempo), with
the two source calls running in parallel; the "docviewer" dashboard shows
request and source latency, per-source outcomes, breaker state and
coalesced requests.

| Status | Code | When |
|--------|------|------|
| 200 | none | At least one system answered, even with nothing; `sources` gives each system's status (`ok`, `timeout`, `error`, `done`, `unavailable`) |
| 400 | `invalid_vin`, `invalid_limit`, `invalid_cursor`, `cursor_outdated` | A bad request; `cursor_outdated` means start again from the first page |
| 404 | `not_found` | Unknown route |
| 502 | `sources_unavailable` | No system answered and the page can still be helped: a retry may work, or nothing has been shown yet. If every remaining system has rejected the request permanently on a later page, the list ends with a 200 instead: no documents, the systems marked `unavailable`, `next_cursor` null |
| 500 | `internal_error` | Anything unexpected |

Every traced response, success or error, carries an `X-Trace-Id` header: the
request's trace ID, which also appears on its log lines and in Tempo. Quote it
when reporting a failure. The service always samples its own traces, so the ID
always resolves in Tempo. It is exposed to browsers through CORS, and the demo
UI shows it in its error notices. The health probes are not traced and have none
(HLD decision D-14, failure correlation).

## Demo UI

`web/` is a static page (HTML, CSS and plain JavaScript, no build step) that
nginx serves on `http://localhost:5173`. It exists to exercise the API by
hand and to make the failure behaviour visible:

- search by VIN, with the fixture vehicles one click away and a page size
  selector; results grouped by month with source and type badges, and
  client-side filters by source, type and text;
- a status strip with each system's outcome on the last page, the trace ID
  with a copy button and a link that opens the trace in Grafana;
- "Load more", optionally automatic as you scroll;
- **Demo controls:** a panel that drives the mocks' fault switches (normal,
  slow, flaky, 503, 403, hang) per system, so you can watch a partial page,
  the catch-up on the next page, the circuit breaker opening and the
  recovery after the cooldown, without leaving the browser;
- a request log with status, duration, per-source outcomes and a trace link
  for every call the page made.

The bases are the current host on the usual ports; override any of them with
`?api=`, `?sales=`, `?service=` or `?grafana=`. The UI is out of scope for
the design documents and has no tests of its own; the API contract it relies
on is covered by the end-to-end tests.

## Repository layout

```
cmd/          the service and the two mock servers
internal/     handler/api (HTTP), aggregator (paging across sources), client (upstream APIs),
              model (domain), config, telemetry, mock (test harness)
pkg/          generic packages: configload, httpjson, resilience, redact, otelsetup
configs/      YAML defaults for the service and the mocks
api/          OpenAPI spec
docs/         design documents (PRD, HLD, LLD), served by GitHub Pages
test/         end-to-end tests
deploy/       Dockerfile, docker-compose, Grafana dashboard
web/          demo UI
```

## Design

The design was written before the code and kept in step with it. It is in
`docs/` as HTML, published with GitHub Pages; start at `docs/index.html`.
Diagrams are pre-rendered SVG, so the files also work offline. Read in order:

1. **PRD** (`prd.html`): the product view: what the viewer does and how it
   behaves, the assumptions that fill the gaps (A-01 to A-08) and the
   acceptance criteria (AC-01 to AC-12) the end-to-end tests are named after.
2. **HLD** (`hld.html`): components, API, paging cursor, failure handling,
   observability, security, and the decisions (D-01 to D-14) with their
   reasons.
3. **LLD** (`lld.html`): repository layout, packages, core types, key logic,
   configuration, mock systems, test plan and implementation order.

Code comments and test names cite these identifiers, so a requirement can be
followed to the test that proves it.
