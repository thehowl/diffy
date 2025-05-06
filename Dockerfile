FROM golang:1.23.2-alpine AS builder

WORKDIR /app

# Add non-priviledged user.
RUN adduser -H -D -u 1000 diffy
# Add data directory
RUN mkdir -p /data && chown 1000:1000 /data

COPY go.mod go.sum ./

RUN go mod download
RUN go mod verify

COPY . .

RUN GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /go/bin/diffy main.go


FROM scratch

COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /go/bin/diffy /go/bin/diffy
COPY --chown=1000:1000 --from=builder /data /data

USER 1000
ENTRYPOINT ["/go/bin/diffy"]
