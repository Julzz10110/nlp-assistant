FROM golang:1.25 AS builder

WORKDIR /src

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/extractor ./services/extractor

FROM gcr.io/distroless/base-debian12

WORKDIR /app

COPY --from=builder /bin/extractor /app/extractor

EXPOSE 50053

ENTRYPOINT ["/app/extractor"]

