FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
WORKDIR /src/examples/whole-enchilada/events/receiver
RUN go build -o /out/receiver ./

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/receiver /usr/local/bin/receiver
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/receiver"]
