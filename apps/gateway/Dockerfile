FROM docker.io/library/golang:1.26 AS build

WORKDIR /src
COPY go.work ./
COPY libs/go-core/go.mod libs/go-core/go.sum ./libs/go-core/
COPY libs/contracts/go.mod ./libs/contracts/
COPY apps/api/go.mod apps/api/go.sum ./apps/api/
COPY apps/gateway/go.mod ./apps/gateway/
COPY apps/worker/go.mod apps/worker/go.sum ./apps/worker/
COPY services/go.mod ./services/
RUN go work sync
COPY libs ./libs
COPY apps/gateway ./apps/gateway
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/gateway ./apps/gateway/cmd/gateway

FROM docker.io/library/alpine:3.22
COPY --from=build /out/gateway /gateway
RUN adduser -D -H -u 65532 app
EXPOSE 8000
USER app
ENTRYPOINT ["/gateway"]
