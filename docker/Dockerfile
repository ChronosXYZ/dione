FROM golang:1.16.6-alpine3.14 as builder

RUN mkdir /app
COPY . /app
WORKDIR /app

RUN apk add build-base
RUN apk add linux-headers
RUN go mod download

RUN go build -v ./cmd/dione/dione.go

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/dione .
COPY docker/entrypoint.sh .
RUN chmod +x /app/entrypoint.sh
CMD ["/app/entrypoint.sh"]