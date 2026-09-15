FROM docker.io/library/golang:1.26 AS build

WORKDIR /src
COPY packages/contracts/go.mod ./packages/contracts/
COPY packages/backend/go.mod packages/backend/go.sum ./packages/backend/
COPY apps/billing/go.mod ./apps/billing/
WORKDIR /src/apps/billing
RUN GOWORK=off go mod download
WORKDIR /src
COPY packages/contracts ./packages/contracts
COPY packages/backend ./packages/backend
COPY apps/billing ./apps/billing
RUN cd apps/billing && GOWORK=off CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/service ./cmd/billing

FROM docker.io/library/alpine:3.22
COPY --from=build /out/service /service
RUN adduser -D -H -u 65532 app
EXPOSE 8080
USER app
ENTRYPOINT ["/service"]
