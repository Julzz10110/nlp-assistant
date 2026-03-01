FROM golang:1.25 AS builder

WORKDIR /src

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/reminder ./services/reminder

FROM gcr.io/distroless/base-debian12

WORKDIR /app

COPY --from=builder /bin/reminder /app/reminder

EXPOSE 50055

ENTRYPOINT ["/app/reminder"]

