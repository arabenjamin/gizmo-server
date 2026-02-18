FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o gizmo-server .

FROM alpine:3.20
WORKDIR /app
COPY --from=builder /app/gizmo-server .
EXPOSE 9090
ENTRYPOINT ["./gizmo-server"]
