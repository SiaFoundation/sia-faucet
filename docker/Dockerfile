# Helper image to build faucetd.
FROM golang:1.26 AS builder

WORKDIR /faucetd

# Copy and build binary.
COPY . .
RUN CGO_ENABLED=1 go build -o bin/ -tags='netgo' -trimpath -a -ldflags '-linkmode external -extldflags "-static"'  ./cmd/faucetd

# Build image that will be used to run faucetd.
FROM alpine
LABEL maintainer="The Sia Foundation <info@sia.tech>"

ENV PUID=0
ENV PGID=0

ENV FAUCETD_RECOVERY_PHRASE=

# Copy binary and prepare data dir.
COPY --from=builder /faucetd/bin/faucetd /usr/bin/
VOLUME [ "/data" ]

EXPOSE 8080/tcp

USER ${PUID}:${PGID}

ENTRYPOINT [ "faucetd", "--dir", "/data" ]
