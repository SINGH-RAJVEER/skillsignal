FROM docker.io/library/golang:1.26 AS build

WORKDIR /src
COPY go.work go.work.sum ./
COPY packages ./packages
COPY apps ./apps
RUN mkdir -p /out && for name in supervisor gateway identity hiring documents evaluation billing worker; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags='-s -w' -o "/out/$name" "./apps/$name/cmd/$name" || exit 1; \
	done

# No application secrets enter the image; the edge Worker passes them at start.
FROM docker.io/library/alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /out/ /app/
USER 65532:65532
EXPOSE 8000
ENTRYPOINT ["/app/supervisor"]
