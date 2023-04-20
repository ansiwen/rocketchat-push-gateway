FROM golang:alpine AS builder

ADD src /src

WORKDIR /src

RUN go build -ldflags="-s -w" .

FROM alpine

RUN adduser rcpg -D -H

COPY --from=builder /src/rocketchat-push-gateway /

ADD container.env /.env

VOLUME [ "/data" ]
USER rcpg
ENTRYPOINT [ "/rocketchat-push-gateway" ]
