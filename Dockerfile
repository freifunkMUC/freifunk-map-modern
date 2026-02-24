FROM golang:1.22-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /freifunk-map .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=build /freifunk-map /freifunk-map
COPY config.example.json /config.json
EXPOSE 8080
ENTRYPOINT ["/freifunk-map", "/config.json"]
