FROM alpine:3.22

RUN wget -O- https://github.com/cozystack/cozyhr/raw/refs/heads/main/hack/install.sh | sh -s -- -v 1.5.0

RUN apk add --no-cache kubectl helm coreutils git jq ca-certificates bash curl

COPY migrations /migrations
COPY run-migrations.sh /usr/bin/run-migrations.sh

WORKDIR /migrations

ENTRYPOINT ["/usr/bin/run-migrations.sh"]
