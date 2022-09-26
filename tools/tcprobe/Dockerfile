FROM golang:1.19 as build

COPY go.mod main.go /workspace/
RUN cd /workspace/ && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o tcprobe

FROM scratch

COPY --from=build /workspace/tcprobe /
ENTRYPOINT ["/tcprobe"]
