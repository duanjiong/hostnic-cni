FROM alpine
RUN apk --no-cache add iptables ipset ca-certificates \
    && update-ca-certificates 2>/dev/null || true
WORKDIR /app

ADD bin .
ADD scripts .

ENTRYPOINT [ "sh /app/install_hostnic.sh" ]







