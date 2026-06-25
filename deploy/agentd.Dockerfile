# Build context: agent-library/go  (the agentkit module root).
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/agentd ./cmd/agentd

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/agentd /usr/local/bin/agentd
ENV ADDR=:8099
EXPOSE 8099
ENTRYPOINT ["/usr/local/bin/agentd"]
