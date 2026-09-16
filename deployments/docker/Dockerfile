# goThrottle — FIFO rate/concurrency queue proxy in front of new-api
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY main.go ./
ARG VERSION=dev
ARG GIT_COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.gitCommit=${GIT_COMMIT}" \
      -o /out/gothrottle .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/gothrottle /usr/local/bin/gothrottle
USER nobody
EXPOSE 3000
ENTRYPOINT ["/usr/local/bin/gothrottle"]
