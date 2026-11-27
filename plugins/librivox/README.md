# LibriVox plugin for kithara-server

Search [LibriVox](https://librivox.org)'s free, public-domain audiobooks from the
Kithara apps and the web page, and bring them into a library. The recordings come from
the Internet Archive, which keeps LibriVox's collection.

It is also the worked example for anyone writing a plugin: about 700 lines of Go with
nothing but the standard library. See
[docs/writing-a-server-plugin.md](../../docs/writing-a-server-plugin.md).

## Running it

Beside the server, in the same Compose file, sharing the incoming folder:

```yaml
services:
  kithara:
    image: ghcr.io/philcharitou/kithara-server:0.9
    volumes:
      - ./books:/books
      - kithara-data:/data
      - incoming:/incoming
    # …as before
  librivox:
    image: ghcr.io/philcharitou/kithara-plugin-librivox:latest
    restart: unless-stopped
    environment:
      KITHARA_PLUGIN_SECRET: ${LIBRIVOX_SECRET}   # 16 characters or more
    volumes:
      - incoming:/incoming
volumes:
  kithara-data:
  incoming:
```

Put `LIBRIVOX_SECRET=` and a long random string in `.env` (`openssl rand -hex 24` makes
one), run `docker compose up -d`, then in the web page go to Settings → Plugins and add
`http://librivox:9000` with the same secret. Search appears in the sidebar.

Without Docker: `go build` here, then run it with `KITHARA_PLUGIN_SECRET` set and
`KITHARA_INCOMING` pointing at the server's incoming folder (the data folder's
`incoming`, unless the server has `KITHARA_INCOMING`).

## Settings

In Settings → Plugins, under LibriVox:

- **Download as:** one M4B file with chapters where LibriVox has made one (most books),
  else MP3; or always MP3 at 64 kbps; or MP3 at the best quality there is.
- **Language:** show only recordings in one language.

## What it does

| Call | |
|---|---|
| Search | The Internet Archive's `librivoxaudio` collection, by title and author; the newest first when nothing is typed. 24 a page. |
| Preview | The recording's description, running time, and the files and size of the chosen format. |
| Download | The audio files, one after another, then the cover. Two downloads at a time; files that fail are tried three times. |

It keeps its downloads in memory: if it restarts while one is running, that download
shows as failed and can be started again.
