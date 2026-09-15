FROM docker.io/library/golang:1.26 AS build

WORKDIR /src
COPY packages/contracts/go.mod ./packages/contracts/
COPY packages/go-core/go.mod packages/go-core/go.sum ./packages/go-core/
COPY apps/worker/go.mod apps/worker/go.sum ./apps/worker/
WORKDIR /src/apps/worker
RUN GOWORK=off go mod download
WORKDIR /src
COPY packages/contracts ./packages/contracts
COPY packages/go-core ./packages/go-core
COPY apps/worker ./apps/worker
RUN cd apps/worker && GOWORK=off CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/skillsignal-worker ./cmd/worker

FROM docker.io/library/alpine:3.22
COPY --from=build /out/skillsignal-worker /skillsignal-worker
RUN adduser -D -H -u 65532 app && mkdir -p /var/lib/skillsignal && chown app:app /var/lib/skillsignal
USER app
ENTRYPOINT ["/skillsignal-worker"]
