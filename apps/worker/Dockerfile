FROM docker.io/library/golang:1.26 AS build

WORKDIR /src
COPY go.work ./
COPY libs/go-core/go.mod libs/go-core/go.sum ./libs/go-core/
COPY apps/api/go.mod apps/api/go.sum ./apps/api/
COPY apps/worker/go.mod apps/worker/go.sum ./apps/worker/
RUN go work sync
COPY libs ./libs
COPY apps/worker ./apps/worker
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/skillsignal-worker ./apps/worker/cmd/worker

FROM docker.io/library/alpine:3.22
COPY --from=build /out/skillsignal-worker /skillsignal-worker
RUN adduser -D -H -u 65532 app && mkdir -p /var/lib/skillsignal && chown app:app /var/lib/skillsignal
USER app
ENTRYPOINT ["/skillsignal-worker"]
