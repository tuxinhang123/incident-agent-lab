FROM golang:1.25-alpine AS build
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
