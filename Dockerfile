# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /zip-watcher .

FROM alpine:3.22
RUN mkdir -p /watch /output /state && chown -R 99:100 /watch /output /state
COPY --from=build /zip-watcher /zip-watcher
USER 99:100
ENTRYPOINT ["/zip-watcher"]
