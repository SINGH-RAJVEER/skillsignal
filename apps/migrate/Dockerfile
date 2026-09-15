FROM docker.io/library/golang:1.26 AS build

WORKDIR /src
COPY packages/contracts/go.mod ./packages/contracts/
COPY packages/backend/go.mod packages/backend/go.sum ./packages/backend/
COPY apps/migrate/go.mod apps/migrate/go.sum ./apps/migrate/
WORKDIR /src/apps/migrate
RUN GOWORK=off go mod download
WORKDIR /src
COPY packages/contracts ./packages/contracts
COPY packages/backend ./packages/backend
COPY apps/migrate ./apps/migrate
RUN cd apps/migrate && GOWORK=off CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/skillsignal-migrate ./cmd/migrate

FROM docker.io/library/alpine:3.22
COPY --from=build /out/skillsignal-migrate /skillsignal-migrate
RUN adduser -D -H -u 65532 app
USER app
ENTRYPOINT ["/skillsignal-migrate"]
