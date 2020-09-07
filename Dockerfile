FROM golang:alpine as builder

COPY main.go .
RUN go build -o smee main.go

FROM alpine as runtime
COPY --from=builder /go/smee .

ENTRYPOINT ["./smee"]

