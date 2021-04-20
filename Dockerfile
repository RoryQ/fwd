FROM golang:alpine as builder

WORKDIR /workspace
COPY go.mod go.sum ./
COPY *.go ./
RUN go build -o smee .

FROM alpine as runtime
COPY --from=builder /workspace/smee .

ENTRYPOINT ["./smee"]

