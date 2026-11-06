FROM golang:1.26.10-alpine@sha256:87c65a688975c0b1d4585d204874c45ca78354670e3a5f11368c5a492303d696 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /freifunk-map .

FROM alpine:3.24@sha256:3f4d6b32e26703c6082839234f43769d5046b3ac7c910033911d1e71d8cd630a
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
