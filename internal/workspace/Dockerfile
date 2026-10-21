# Talaria's workspace: a small Linux computer the agent keeps between runs, with
# a browser. Files live in the /workspace volume, so they outlive the container.
FROM alpine:3.20
RUN apk add --no-cache chromium font-noto-emoji ttf-freefont ca-certificates tini \
        bash curl git jq openssh-client python3 unzip \
    && adduser -D -u 10001 -h /home/talaria talaria \
    && mkdir -p /workspace && chown talaria /workspace
USER talaria
ENV HOME=/home/talaria
WORKDIR /workspace
LABEL talaria.workspace="1"
CMD ["tail", "-f", "/dev/null"]
