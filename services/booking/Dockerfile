FROM golang:1.25 AS builder

WORKDIR /src

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/booking ./services/booking

FROM gcr.io/distroless/base-debian12

WORKDIR /app

COPY --from=builder /bin/booking /app/booking

EXPOSE 50056

ENTRYPOINT ["/app/booking"]
