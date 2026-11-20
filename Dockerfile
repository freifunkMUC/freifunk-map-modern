FROM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /freifunk-map .

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk add --no-cache ca-certificates && mkdir /data && chown 65532:65532 /data
COPY --from=build /freifunk-map /freifunk-map
COPY config.example.json /config.json
COPY config.federation.json /config.federation.json
# The caches are written to the working directory.
WORKDIR /data
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/freifunk-map"]
CMD ["/config.json"]
