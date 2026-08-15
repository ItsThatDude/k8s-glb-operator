FROM haproxy:latest

USER root

RUN mkdir -p /var/run/haproxy && \
    chown -R 1000:1000 /var/run/haproxy && \
    mkdir -p /etc/haproxy && \
    chown -R 1000:1000 /etc/haproxy

USER 1000