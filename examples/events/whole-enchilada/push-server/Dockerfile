FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
WORKDIR /src/examples/events/whole-enchilada/push-server
RUN go build -o /out/push-server ./

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/push-server /usr/local/bin/push-server
ENTRYPOINT ["/usr/local/bin/push-server"]
