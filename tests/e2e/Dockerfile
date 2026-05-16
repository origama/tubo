FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends bash ca-certificates curl coreutils procps iproute2 lsof \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /work
COPY bin/tubo /usr/local/bin/tubo
COPY bin/dummy-api-server /usr/local/bin/dummy-api-server
RUN chmod +x /usr/local/bin/tubo /usr/local/bin/dummy-api-server

ENTRYPOINT ["sleep", "infinity"]
