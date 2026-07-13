FROM golang:1.24-bookworm AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -tags sqlite_fts5 -o /out/archive ./cmd/archive

FROM debian:bookworm-slim

RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates poppler-utils \
  && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=build /out/archive /app/archive
COPY web /app/web
RUN mkdir -p /app/data

ENV ARCHIVE_ADDR=:8080
ENV ARCHIVE_DATA=/app/data
EXPOSE 8080

CMD ["/app/archive"]
