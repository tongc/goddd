FROM golang:1.17.5-alpine as build-env
WORKDIR /go/src/github.com/tongc/goddd/
COPY . .
RUN apk add --update --no-cache git
RUN CGO_ENABLED=0 go mod vendor
RUN CGO_ENABLED=0 go get -ldflags "-s -w -extldflags '-static'" github.com/go-delve/delve/cmd/dlv
RUN CGO_ENABLED=0 GOOS=linux go build -mod vendor -mod=mod -gcflags "all=-N -l" -a -installsuffix cgo -o goapp ./cmd/shippingsvc

FROM alpine:3.15.0
WORKDIR /app
COPY --from=build-env /go/bin/dlv /dlv
COPY --from=build-env /go/src/github.com/tongc/goddd/booking/docs ./booking/docs
COPY --from=build-env /go/src/github.com/tongc/goddd/tracking/docs ./tracking/docs
COPY --from=build-env /go/src/github.com/tongc/goddd/handling/docs ./handling/docs
COPY --from=build-env /go/src/github.com/tongc/goddd/goapp .
EXPOSE 8080
ENTRYPOINT [ "/dlv" ]
