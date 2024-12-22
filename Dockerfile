FROM golang:1.21.13-alpine3.20@sha256:2414035b086e3c42b99654c8b26e6f5b1b1598080d65fd03c7f499552ff4dc94 as builder

WORKDIR /app

# Add non-priviledged user.
RUN adduser -H -D -u 1000 diffy
# Add data directory
RUN mkdir -p /data && chown 1000:1000 /data

COPY go.mod go.sum ./

RUN go mod download
RUN go mod verify

COPY main.go ./main.go
COPY pkg ./pkg
COPY templates ./templates

RUN GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /go/bin/diffy main.go


FROM scratch

COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /go/bin/diffy /go/bin/diffy
COPY --chown=1000:1000 --from=builder /data /data

USER 1000
ENTRYPOINT ["/go/bin/diffy"]
