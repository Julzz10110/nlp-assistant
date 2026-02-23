FROM golang:1.25 AS builder

WORKDIR /src

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/classifier ./services/classifier

FROM gcr.io/distroless/base-debian12

WORKDIR /app

COPY --from=builder /bin/classifier /app/classifier

EXPOSE 50052

ENTRYPOINT ["/app/classifier"]

