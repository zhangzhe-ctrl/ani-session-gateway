# syntax=docker/dockerfile:1
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY api/go.mod api/go.sum ./api/
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/session-gateway ./cmd/session-gateway

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/session-gateway /session-gateway
USER nonroot:nonroot
ENTRYPOINT ["/session-gateway"]
