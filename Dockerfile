# syntax=docker/dockerfile:1

FROM golang:1.25 AS build
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/server ./cmd/server

# Runtime (needs git)
FROM alpine:3.23.2
RUN apk add --no-cache ca-certificates git curl
WORKDIR /srv
COPY --from=build /out/server /srv/server
EXPOSE 8080
ENTRYPOINT ["/srv/server"]
