## NLP Assistant

A distributed personal assistant written in Go that understands natural‑language weather questions, routes them through a small set of gRPC microservices, and exposes full end‑to‑end tracing via OpenTelemetry and Jaeger.  
This repository implements the **Phase 1 MVP** of the larger specification: orchestrator + rule‑based NLP + weather business service, with a console client and Docker Compose setup.

### High‑level architecture

- **Console Client (`services/client`)**
  - Simple CLI that opens a bidirectional gRPC stream to the orchestrator (`Converse`) and lets you chat from the terminal.

- **Orchestrator (`services/orchestrator`)**
  - gRPC server implementing `assistant.ConversationOrchestrator`.
  - Manages dialog state via an internal FSM (`NEW → PROCESSING → COLLECTING → COMPLETED`).
  - Calls NLP microservices:
    - `IntentClassifier` — determines user intent.
    - `EntityExtractor` — extracts entities (e.g. `location`).
  - Invokes business service:
    - `WeatherService` — returns a synthetic weather response.
  - Emits OpenTelemetry spans with semantic attributes (session, user, intent, etc.).

- **NLP microservices**
  - **Classifier (`services/classifier`)**
    - Rule‑based: detects `get_weather` intent when the message contains words like `"weather"` or `"rain"`.
  - **Entity Extractor (`services/extractor`)**
    - Rule‑based: extracts `location = "Tokyo"` when the utterance contains `"tokyo"`.

- **Business service**
  - **Weather (`services/weather`)**
    - Read‑only service that generates pseudo‑random weather for the requested location and date.

- **Observability**
  - **OpenTelemetry** for tracing (gRPC server + client interceptors).
  - **Jaeger** (all‑in‑one) for trace visualization (exposed via Docker Compose).

### Protobuf contracts

All contracts live in `api/proto/assistant.proto` and are generated into `api/proto/assistantpb`.

Key services:

- `ConversationOrchestrator`
  - `Converse(stream ClientMessage) returns (stream ServerMessage)` — main bidirectional dialog stream.
  - `GetHistory(HistoryRequest) returns (HistoryResponse)` — returns stored session messages.
- `IntentClassifier`
  - `Classify(ClassifyRequest) returns (ClassifyResponse)`
- `EntityExtractor`
  - `Extract(ExtractRequest) returns (ExtractResponse)`
- `WeatherService`
  - `GetWeather(WeatherRequest) returns (WeatherResponse)`

### Tech stack

- **Language**: Go
- **RPC**: gRPC + Protocol Buffers
- **State management**: in‑memory state store in orchestrator (interface designed to be backed by Redis later)
- **Observability**: OpenTelemetry SDK + Jaeger
- **Containerization**: Docker + Docker Compose

### Project structure

```text
.
├── api/
│   └── proto/
│       ├── assistant.proto          # Protobuf contracts
│       └── assistantpb/             # Generated Go code
├── config/
│   └── orchestrator.yaml            # Orchestrator configuration (ports, service addresses, OTEL)
├── internal/
│   ├── config/                      # YAML config loading
│   └── telemetry/                   # OpenTelemetry initialization helper
├── services/
│   ├── orchestrator/                # Core orchestrator (FSM + routing + OTel)
│   ├── classifier/                  # Rule-based intent classifier
│   ├── extractor/                   # Rule-based entity extractor
│   ├── weather/                     # Weather business service
│   └── client/                      # Console gRPC client
├── docker-compose.yaml
├── go.mod
└── go.sum
```

### Running locally without Docker

Requirements:

- Go toolchain (version matching `go.mod`, or newer)
- `protoc` with `protoc-gen-go` and `protoc-gen-go-grpc` installed (already used to generate `assistantpb`)

From the project root:

```bash
# 1. Start NLP services and weather service (in separate terminals)
go run ./services/classifier
go run ./services/extractor
go run ./services/weather

# 2. Start orchestrator
go run ./services/orchestrator

# 3. Start console client
go run ./services/client
```

By default:

- Orchestrator listens on `localhost:50051`.
- Classifier on `localhost:50052`.
- Extractor on `localhost:50053`.
- Weather service on `localhost:50054`.

These ports are wired in `config/orchestrator.yaml`.

### Running with Docker Compose

Requirements:

- Docker
- Docker Compose (v2 or compatible)

From the project root:

```bash
docker-compose build
docker-compose up
```

This will start:

- `nlp-classifier` on `50052`
- `nlp-extractor` on `50053`
- `nlp-weather` on `50054`
- `nlp-orchestrator` on `50051`
- `nlp-jaeger` with UI on `16686` and OTLP HTTP on `4318`

After the stack is running, you can connect the console client from your host:

```bash
ORCHESTRATOR_ADDR=localhost:50051 go run ./services/client
```

### Example dialog

In the console client, try messages like:

- `What is the weather in Tokyo today?`
- `Will it rain in Tokyo?`

The typical flow:

1. Client sends a `ClientMessage` via `Converse`.
2. Orchestrator:
   - Loads/creates session state.
   - Calls `IntentClassifier` → detects `get_weather`.
   - Calls `EntityExtractor` → extracts `location = "Tokyo"` (if present).
   - Calls `WeatherService` → receives synthetic weather.
3. Orchestrator replies with `ServerMessage.Text` containing a human‑readable summary.

If `location` cannot be inferred, orchestrator sends a `ConfirmationRequest` asking:

> `For which location do you need the weather?`

### Observability (Jaeger)

When running with Docker Compose, Jaeger is available at:

- `http://localhost:16686`

Search for the `orchestrator` service to see full traces:

- Conversation spans for each dialog turn.
- Child spans for NLP calls and weather service.
- Custom attributes like `assistant.session_id`, `assistant.user_id`, and intent‑related data.

### Future extensions

This MVP is intentionally focused on the simplest end‑to‑end path (weather intent only).  
The following steps would align with the original specification:

- Add additional business services (e.g. booking, reminders).
- Introduce a real Redis‑backed state store.
- Implement full dialog FSM with richer `COLLECTING` and `CONFIRMING` behavior.
- Add a Saga coordinator for distributed transactions (e.g. booking workflows).
- Add an API Gateway/BFF for web/mobile clients (and potentially gRPC‑Web or WebSockets).

