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
  - Invokes business services:
    - `WeatherService` — synthetic weather.
    - `ReminderService` — create/list/cancel reminders.
    - `BookingService` — reserve/confirm/cancel table bookings (Saga: Reserve → Confirm; on failure, Cancel as compensation).
  - Emits OpenTelemetry spans with semantic attributes (session, user, intent, etc.).

- **NLP microservices**
  - **Classifier (`services/classifier`)** — rule‑based: `get_weather`, `create_reminder`, `book_table`.
  - **Entity Extractor (`services/extractor`)** — rule‑based: `location`, `text`, `place`, `persons`.

- **Business services**
  - **Weather (`services/weather`)** — synthetic weather.
  - **Reminder (`services/reminder`)** — in‑memory reminders.
  - **Booking (`services/booking`)** — in‑memory bookings; Reserve/Confirm/Cancel for Saga.

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
- `WeatherService` — `GetWeather(...)`
- `ReminderService` — `CreateReminder`, `ListReminders`, `CancelReminder`
- `BookingService` — `Reserve`, `Confirm`, `Cancel` (Saga participant)

After editing `api/proto/assistant.proto`, regenerate Go code so that `assistantpb` gets types like `Booking`, `BookingServiceClient`, etc.


### Tech stack

- **Language**: Go
- **RPC**: gRPC + Protocol Buffers
- **State management**: in‑memory state store in orchestrator (interface designed to be backed by Redis later)
- **Observability**: OpenTelemetry SDK + Jaeger
- **Containerization**: Docker + Docker Compose

### Running locally without Docker

Requirements:

- Go toolchain (version matching `go.mod`, or newer)
- `protoc` with `protoc-gen-go` and `protoc-gen-go-grpc` installed (already used to generate `assistantpb`)

From the project root:

```bash
# 1. Start NLP and business services (in separate terminals)
go run ./services/classifier
go run ./services/extractor
go run ./services/weather
go run ./services/reminder
go run ./services/booking

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

