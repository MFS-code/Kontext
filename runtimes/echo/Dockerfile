FROM alpine:3.21
RUN apk add --no-cache bash coreutils jq
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
