FROM docker.io/library/golang:1.26 AS build

WORKDIR /src
COPY packages/contracts/go.mod ./packages/contracts/
COPY packages/go-core/go.mod packages/go-core/go.sum ./packages/go-core/
COPY apps/gateway/go.mod ./apps/gateway/
WORKDIR /src/apps/gateway
RUN GOWORK=off go mod download
WORKDIR /src
COPY packages/contracts ./packages/contracts
COPY packages/go-core ./packages/go-core
COPY apps/gateway ./apps/gateway
RUN cd apps/gateway && GOWORK=off CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/gateway ./cmd/gateway

FROM docker.io/library/alpine:3.22
COPY --from=build /out/gateway /gateway
RUN adduser -D -H -u 65532 app
EXPOSE 8000
USER app
ENTRYPOINT ["/gateway"]
