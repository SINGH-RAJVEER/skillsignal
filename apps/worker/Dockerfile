# syntax=docker/dockerfile:1.7
FROM docker.io/library/golang:1.24-alpine AS build
WORKDIR /src
COPY libs/go-core ./libs/go-core
COPY apps/worker ./apps/worker
WORKDIR /src/apps/worker
RUN go mod download && CGO_ENABLED=0 go build -trimpath -o /out/worker ./cmd/worker

FROM docker.io/library/alpine:3.21
RUN adduser -D -u 10001 app
WORKDIR /app
COPY --from=build --chown=app:app /out/worker ./worker
USER app
CMD ["./worker"]
