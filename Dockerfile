FROM gcr.io/distroless/static

COPY bin/manager /usr/bin/

ENTRYPOINT /usr/bin/manager
