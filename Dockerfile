FROM alpine
ARG TARGETPLATFORM
WORKDIR /

COPY $TARGETPLATFORM/odoo-operator /odoo-operator
USER 65532:65532

ENTRYPOINT ["/odoo-operator"]
