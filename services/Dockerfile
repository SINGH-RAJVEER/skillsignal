FROM docker.io/library/golang:1.26 AS build

ARG SERVICE
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
COPY services ./services
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/service ./services/cmd/${SERVICE}

FROM docker.io/library/alpine:3.22
COPY --from=build /out/service /service
RUN adduser -D -H -u 65532 app
EXPOSE 8080
USER app
ENTRYPOINT ["/service"]
