ARG GO_VERSION=1.27.1
FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY api ./api
COPY cli ./cli
COPY cmd ./cmd
COPY engine ./engine
COPY flows ./flows
COPY keystore ./keystore
COPY message ./message
COPY steps ./steps
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/dif ./cmd/dif

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/dif /dif
USER 65532:65532
WORKDIR /data
STOPSIGNAL SIGTERM
ENTRYPOINT ["/dif"]
CMD ["run"]
