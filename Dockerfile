# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
# /metrics の ts2560_dashboard_build_info に出る版。CI ではイメージのタグが入る
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY web ./web
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/ts2560-dashboard . \
 && mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ts2560-dashboard /ts2560-dashboard
# 履歴（推移グラフと空の受信記録）の置き場所。ボリュームをつなぐと再起動しても残る
COPY --from=build --chown=65532:65532 /out/data /data
ENV TS2560_BIND=0.0.0.0 \
    TS2560_DATA=/data \
    TS2560_CONFIG=/config/config.json
EXPOSE 8560
USER nonroot:nonroot
ENTRYPOINT ["/ts2560-dashboard"]
