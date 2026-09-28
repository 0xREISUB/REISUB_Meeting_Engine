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
    -o /out/reisub-manager ./cmd/manager

FROM alpine:3.22 AS backend

RUN apk add --no-cache ca-certificates wget \
    && addgroup -S -g 10001 app \
    && adduser -S -D -H -u 10001 -G app app \
    && mkdir -p /data \
    && chown 10001:10001 /data

COPY --from=build /out/reisub-backend /usr/local/bin/reisub-backend

USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/reisub-backend"]

FROM alpine:3.22 AS manager

RUN apk add --no-cache ca-certificates wget docker-cli docker-cli-compose

COPY --from=build /out/reisub-manager /usr/local/bin/reisub-manager

EXPOSE 8090
ENTRYPOINT ["/usr/local/bin/reisub-manager"]