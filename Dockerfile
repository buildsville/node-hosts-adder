FROM golang:alpine AS builder

RUN apk update && \
    apk add git build-base && \
    rm -rf /var/cache/apk/* && \
    mkdir -p "$GOPATH/src/github.com/buildsville/" && \
    git clone https://github.com/buildsville/node-hosts-converter.git && \
    mv node-hosts-converter "$GOPATH/src/github.com/buildsville/" && \
    cd "$GOPATH/src/github.com/buildsville/node-hosts-converter" && \
    GOOS=linux GOARCH=amd64 go build -o /node-hosts-converter

FROM alpine:3.7

RUN apk add --update ca-certificates

COPY --from=builder /node-hosts-converter /node-hosts-converter

ENTRYPOINT ["/node-hosts-converter"]
