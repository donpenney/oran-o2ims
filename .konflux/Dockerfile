# Build stage
FROM brew.registry.redhat.io/rh-osbs/openshift-golang-builder:rhel_9_golang_1.23@sha256:4805e1cb2d1bd9d3c5de5d6986056bbda94ca7b01642f721d83d26579d333c60 AS builder

WORKDIR /app
COPY . .

# manager
RUN GOEXPERIMENT=strictfipsruntime CGO_ENABLED=1 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -mod=vendor -tags strictfipsruntime -a -o /bin/oran-o2ims main.go

# Runtime stage
FROM registry.redhat.io/rhel9-4-els/rhel-minimal:9.4@sha256:65e57c845402711c5515af0989a2c3c69bf4066396008efd8002be0790fee6c3

WORKDIR /

COPY --from=builder /bin/oran-o2ims /usr/bin/oran-o2ims

USER 65532:65532
ENTRYPOINT ["/usr/bin/oran-o2ims"]
