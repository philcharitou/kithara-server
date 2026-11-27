FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod *.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /kithara-plugin-librivox .

# User 1000, as in the server's image, so the server can move what this writes.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 1000 kithara \
 && install -d -o kithara -g kithara -m 0777 /incoming
COPY --from=build /kithara-plugin-librivox /usr/local/bin/kithara-plugin-librivox
USER kithara
ENV KITHARA_INCOMING=/incoming KITHARA_PLUGIN_ADDR=:9000
EXPOSE 9000
ENTRYPOINT ["kithara-plugin-librivox"]
