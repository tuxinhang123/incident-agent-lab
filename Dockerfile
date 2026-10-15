FROM golang:1.25-alpine AS build
# GOPROXY 默认为国内镜像，避免 proxy.golang.org 超时；
# 可通过 `docker build --build-arg GOPROXY=...` 或 compose args 覆盖。
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=$GOPROXY GOSUMDB=off
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/demo-api ./cmd/demo-api

FROM alpine:3.20
RUN apk add --no-cache wget
COPY --from=build /out/demo-api /usr/local/bin/demo-api
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/health || exit 1
USER nobody
ENTRYPOINT ["demo-api"]
