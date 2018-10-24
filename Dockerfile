FROM golang:alpine as builder

# git for deps
RUN apk update && apk add git

COPY . $GOPATH/src/proxyconf/
WORKDIR $GOPATH/src/proxyconf/
#get dependancies
#you can also use dep
RUN go get -d -v
#build the binary
RUN go build -o /go/bin/proxyconf

# build final image
FROM alpine

COPY --from=builder /go/bin/proxyconf /usr/bin/proxyconf 
ENTRYPOINT [ "/usr/bin/proxyconf" ]
