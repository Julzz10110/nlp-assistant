FROM golang:1.25 AS builder

WORKDIR /src

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/orchestrator ./services/orchestrator

FROM gcr.io/distroless/base-debian12

WORKDIR /app

COPY --from=builder /bin/orchestrator /app/orchestrator
COPY config/orchestrator.yaml /app/config/orchestrator.yaml

EXPOSE 50051

ENTRYPOINT ["/app/orchestrator"]

