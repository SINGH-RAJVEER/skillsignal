# syntax=docker/dockerfile:1.7
FROM docker.io/library/golang:1.24-alpine AS build
WORKDIR /src
COPY libs/go-core ./libs/go-core
COPY apps/api ./apps/api
WORKDIR /src/apps/api
RUN go mod download && CGO_ENABLED=0 go build -trimpath -o /out/api ./cmd/api

FROM docker.io/library/alpine:3.21
RUN adduser -D -u 10001 app && mkdir -p /var/resume-storage && chown app:app /var/resume-storage
WORKDIR /app
COPY --from=build --chown=app:app /out/api ./api
USER app
EXPOSE 8000
# Database migrations land with the Go port; until then the API starts directly.
CMD ["./api"]
