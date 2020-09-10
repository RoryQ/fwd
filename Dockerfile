FROM golang:alpine as builder

WORKDIR /workspace
COPY main.go go.mod go.sum ./
RUN go build -o smee main.go

FROM alpine as runtime
COPY --from=builder /workspace/smee .

ENTRYPOINT ["./smee"]

