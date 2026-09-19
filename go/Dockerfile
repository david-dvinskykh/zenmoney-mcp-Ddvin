# Static binary on scratch: nothing to update, nothing to exploit, ~9 MB.
FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go test ./... \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/zenmoney-mcp ./cmd/zenmoney-mcp

FROM scratch

# TLS roots: the server talks to api.zenmoney.ru over HTTPS and scratch has none.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/zenmoney-mcp /zenmoney-mcp

# The cache directory has to be writable; UserHomeDir falls back to "." without
# HOME, so point it somewhere explicit.
ENV HOME=/data ZENMONEY_CACHE_DIR=/data/cache
VOLUME /data

EXPOSE 8081
ENTRYPOINT ["/zenmoney-mcp"]
CMD ["-http", ":8081"]
