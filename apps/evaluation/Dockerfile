FROM docker.io/library/golang:1.26 AS build

WORKDIR /src
COPY packages/contracts/go.mod ./packages/contracts/
COPY packages/go-core/go.mod packages/go-core/go.sum ./packages/go-core/
COPY apps/evaluation/go.mod ./apps/evaluation/
WORKDIR /src/apps/evaluation
RUN GOWORK=off go mod download
WORKDIR /src
COPY packages/contracts ./packages/contracts
COPY packages/go-core ./packages/go-core
COPY apps/evaluation ./apps/evaluation
RUN cd apps/evaluation && GOWORK=off CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/service ./cmd/evaluation

FROM docker.io/library/alpine:3.22
COPY --from=build /out/service /service
RUN adduser -D -H -u 65532 app
EXPOSE 8080
USER app
ENTRYPOINT ["/service"]
