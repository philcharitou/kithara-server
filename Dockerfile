FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY web ./web
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /kithara-server .

# whisper.cpp's command-line program, for transcribing on the server. Built for the
# common baseline of each architecture (AVX2 on x86-64) rather than for the build
# machine's own processor, so the image runs anywhere.
FROM alpine:3.20 AS whisper
ARG WHISPER_VERSION=v1.7.6
RUN apk add --no-cache git cmake make g++ \
 && git clone --depth 1 --branch "$WHISPER_VERSION" https://github.com/ggml-org/whisper.cpp /whisper \
 && cmake -S /whisper -B /whisper/build -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF \
      -DGGML_NATIVE=OFF -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_SERVER=OFF \
 && cmake --build /whisper/build -j"$(nproc)" --target whisper-cli \
 && install -m 755 /whisper/build/bin/whisper-cli /usr/local/bin/whisper-cli

FROM alpine:3.20
RUN apk add --no-cache ffmpeg ca-certificates libstdc++ libgomp && adduser -D -h /data kithara
COPY --from=whisper /usr/local/bin/whisper-cli /usr/local/bin/whisper-cli
COPY --from=build /kithara-server /usr/local/bin/kithara-server
USER kithara
ENV KITHARA_LIBRARY=/library KITHARA_DATA=/data KITHARA_ADDR=:8080
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["kithara-server"]
CMD ["serve"]
