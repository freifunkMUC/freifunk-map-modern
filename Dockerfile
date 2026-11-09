FROM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build
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
