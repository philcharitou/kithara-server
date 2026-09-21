FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /kithara-server .

FROM alpine:3.20
RUN apk add --no-cache ffmpeg ca-certificates && adduser -D -h /data kithara
COPY --from=build /kithara-server /usr/local/bin/kithara-server
USER kithara
ENV KITHARA_LIBRARY=/library KITHARA_DATA=/data KITHARA_ADDR=:8080
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["kithara-server"]
CMD ["serve"]
