FROM golang:1.25-alpine AS build
RUN apk add --no-cache git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/airlock ./cmd/airlock

FROM alpine:3.20
RUN apk add --no-cache bash git curl jq
COPY --from=build /out/airlock /usr/local/bin/airlock
WORKDIR /work
