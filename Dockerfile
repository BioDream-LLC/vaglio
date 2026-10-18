# Vaglio as a container: one static binary on a distroless base, as a non-root user, with its data in /data (a volume:
# the audit log, cases and accounts) and the plan's policies in /policies. Base images pinned by digest.
#
#   docker build -t vaglio .
#   docker run -p 8740:8740 -v vaglio-data:/data -v ./policies:/policies:ro vaglio -hostname vaglio.example.org
FROM golang:1.27.2-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /vaglio ./cmd/vaglio \
 && mkdir -p /out/data /out/policies && chmod 700 /out/data && chown 65532:65532 /out/data

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /vaglio /usr/local/bin/vaglio
# Copied as a tree, so /data keeps its mode 700 and owner (a directory copied on its own would not).
COPY --from=build /out/ /
USER 65532:65532
VOLUME ["/data"]
EXPOSE 8740
ENTRYPOINT ["/usr/local/bin/vaglio", "serve", "-data", "/data", "-policies", "/policies", "-addr", "0.0.0.0:8740"]
