# Build the event-server binary from the multi-module mcpkit tree.
# The Dockerfile must be run with the build context rooted at the
# repo root (the docker-compose.yaml does this with `context: ../../..`).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
WORKDIR /src/examples/whole-enchilada/events/event-server
RUN go build -o /out/event-server ./

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/event-server /usr/local/bin/event-server
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/event-server"]
