# Tafari — WhatsApp bot (whatsmeow) with an LLM brain
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/whatsapp-bot .

FROM alpine:3.20
WORKDIR /app
RUN apk add --no-cache ca-certificates
COPY --from=build /out/whatsapp-bot /app/whatsapp-bot
# The WhatsApp session (whatsapp.db) is account credentials: it is created at
# runtime in TAFARI_DATA_DIR and must never be copied into an image. Mount a
# persistent disk at /app/data so the session survives restarts.
ENV TAFARI_DATA_DIR=/app/data
EXPOSE 8080
CMD ["/app/whatsapp-bot"]
