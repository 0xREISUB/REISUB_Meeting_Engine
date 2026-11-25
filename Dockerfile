FROM golang:1.27-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/reisub-backend . \
    && CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/sqlite-to-postgres ./cmd/sqlite-to-postgres

FROM alpine:3.22 AS backend

RUN apk add --no-cache ca-certificates wget \
    && addgroup -S -g 10001 app \
    && adduser -S -D -H -u 10001 -G app app \
    && mkdir -p /data \
    && chown 10001:10001 /data

COPY --from=build /out/reisub-backend /usr/local/bin/reisub-backend
# One-off: copies a SQLite database into PostgreSQL (see README).
COPY --from=build /out/sqlite-to-postgres /usr/local/bin/sqlite-to-postgres

USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/reisub-backend"]
