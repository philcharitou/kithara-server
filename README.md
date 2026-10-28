# kithara-server

A small, self-hosted server for the [Kithara](https://kithara-app.com) audiobook apps.
Point it at a folder of audiobooks and it becomes a library you can stream or download
from any of your devices, with your listening position and bookmarks following you
between them.

One static binary, no database, no accounts beyond the ones you create. State is a
handful of JSON files. It implements version 1 of the
[Kithara sync protocol](https://kithara-app.com/sync-protocol).

## Requirements

- Linux, macOS or Windows; any architecture Go builds for (releases ship linux/amd64,
  linux/arm64 for a Raspberry Pi or a NAS, macOS and Windows).
- **ffmpeg** on the PATH. `ffprobe` reads durations, tags and chapters; `ffmpeg` pulls
  embedded cover art out of files that have it. (`apt install ffmpeg`, `brew install ffmpeg`,
  or the Windows build from ffmpeg.org.)

## Quick start

```bash
kithara-server user add phil                  # asks for a password (8+ characters)
kithara-server serve --library ~/Audiobooks --data ~/.kithara-server --name "Phil's shelf"
```

Then in Kithara: **Add a library > Kithara sync server**, address `http://your-machine:8080`,
your user name and password. The app checks `/api/health`, signs in, and syncs.

`kithara-server scan --library ~/Audiobooks` prints what the server sees without serving.

**More than one library.** Give `--library` once per folder, optionally named:

```bash
kithara-server serve --library ~/Audiobooks --library Kids=/srv/kids --library Lectures=/srv/talks
```

The apps still see one catalogue (that is what the protocol has); each book carries a
`library` field naming its folder, `GET /api/libraries` lists them with counts, and
`GET /api/library?library=Kids` returns just one. In the environment, separate folders
with `:` (`;` on Windows). A folder that cannot be read at scan time (an unmounted share)
keeps the books from its last good scan instead of vanishing from every device.

## How the library folder is read

- A **folder with audio files directly in it** is a book; the files are its parts, in
  natural order (`part 1`, `part 2`, `part 10`).
- A folder with **only `CD 1`, `CD 2`, `Disc 3`, `Part 4` subfolders** is one book, the
  discs joined in order.
- An **audio file sitting beside book folders**, or at the top level, is a book of its own.
- Any nesting above that is just organisation (`Author/Series/Book/` works).
- Formats: m4b, m4a, mp4, aac, mp3, ogg, opus, flac, wav, wma, mka.
- **Metadata** is gathered from several places, each overriding the one before:
  1. **Folder names.** `Author/Book` and `Author/Series/Book` give author and series; a
     book folder named `3 - Title`, `Book 3 - Title`, `(1999) Title` or
     `Title {Narrator}` gives the series number, year and narrator.
  2. **File tags.** Title from `album` (or `title` for a single file), author from
     `artist`/`album_artist`, narrator from `composer`, series from `series`/`grouping`,
     series number from `series-part`, description from `description`/`comment`, plus
     `genre`, `date`, `publisher`, `language`, `isbn`, `asin` and `subtitle`.
  3. **`desc.txt`** (description) and **`reader.txt`** (narrator) in the book folder.
  4. **An `.opf` file** (`metadata.opf`, or the only `.opf` in the folder), as Calibre
     and audiobookshelf write them.
  5. **`metadata.json`**, audiobookshelf's own format, including its chapter list. A
     library audiobookshelf has already tidied comes across as it was.
- **Chapters** are read from the files (m4b chapter atoms, mp3 chapter frames) and laid
  onto one combined timeline across parts. Files without embedded chapters get one
  chapter each, named from their `title` tag or file name.
- **Cover**: `cover.jpg`/`cover.png`/`folder.jpg` in the folder, else the artwork embedded in
  the first file.
- Each book folder gets a tiny `.kithara-id` file so its id survives renames and moves
  and your listening position stays attached. If the folder is read-only, the id is kept
  in the data folder keyed by path instead (and a rename then looks like a new book).

Changed and new books are picked up every `--rescan` interval (default 10 minutes), on a
`POST /api/rescan` from a signed-in client, and at start. Probing is cached by file size
and modification time, so rescans of a large unchanged library cost almost nothing.

## Data folder

```
users.json            user names and password hashes (PBKDF2-SHA256, 600k rounds)
tokens.json           device tokens, one per sign-in
users/<name>/progress.json
users/<name>/bookmarks.json
users/<name>/sessions.json  listening sessions, worked out from progress
probe-cache.json      ffprobe results, by file
covers/               artwork extracted from files
ids-<library>.json    ids by path, for each library (ids.json in older versions)
```

Back up the `users/` folder; everything else is rebuilt from the library.
Removing a user (`user remove NAME`) signs their devices out and moves their
listening state aside rather than deleting it.

## Running it properly

**As a service (systemd):**

```ini
[Unit]
Description=Kithara audiobook server
After=network.target

[Service]
User=kithara
ExecStart=/usr/local/bin/kithara-server serve
Environment=KITHARA_LIBRARY=/srv/audiobooks
Environment=KITHARA_DATA=/var/lib/kithara-server
Environment=KITHARA_ADDR=127.0.0.1:8080
Environment=KITHARA_NAME=Home
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

**Docker:**

```bash
docker run -d --name kithara \
  -v /srv/audiobooks:/library:ro -v kithara-data:/data -p 8080:8080 \
  ghcr.io/philcharitou/kithara-server
docker exec -it kithara kithara-server user add phil
```

(`:ro` on the library means ids are kept in the data volume instead of sidecar files;
drop it if you want the sidecars.)

**HTTPS.** Plain `http://` is fine on your own Wi-Fi. Anywhere else, put it behind a
reverse proxy that terminates TLS (Caddy does this with two lines; nginx with certbot
works too) and bind the server to `127.0.0.1`. The token in every request is a bearer
credential, so treat the connection like a password.

```
# Caddyfile
audiobooks.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

The proxy must pass `Range` requests through untouched (they all do by default) and
should not buffer responses for large files (`proxy_buffering off;` in nginx keeps
seeking snappy).

## Building

```bash
go build .                               # this machine
GOOS=linux GOARCH=arm64 go build .       # a Raspberry Pi or ARM NAS
go test ./...                            # needs ffmpeg on the PATH
```

Standard library only; no dependencies to fetch.

## Protocol notes for other clients

Everything the app talks is in the protocol document. Server specifics:

- `PUT /api/progress/{bookId}` clamps `positionMs` to the book's duration and answers
  `409` with the stored record when the incoming `updatedAt` is older.
- `GET /api/library` carries an `ETag`; send it back as `If-None-Match` to get `304`
  when nothing has changed.
- `POST /api/rescan` (bearer) triggers a scan and returns the book count.
- `DELETE /api/progress/{bookId}` forgets a position (not used by the app; handy for
  scripts).
- Progress records also carry `startedAt` and `finishedAt`, kept by the server.
- `GET /api/sessions` (optionally `?bookId=` and `?since=` ms) lists listening sessions.
  The apps upload progress when playback pauses, so the stretch between two uploads is
  counted as listening, and joined to the session before it if playback resumed within
  ten minutes. Jumps that playback could not have covered in the time (seeks, a book
  marked finished) count for nothing, so the figures lean low.
- `GET /api/stats?tzOffset=MINUTES` (minutes east of UTC; days begin at local midnight)
  sums those sessions: total time, time per day, per book, days listened, current and
  longest streak, books started and finished.
- `GET /api/libraries` and `GET /api/library?library=NAME`, as above.
- Failed sign-ins are limited to 10 per address and 30 per user name in 15 minutes;
  past that, `POST /api/auth` answers `429` with `Retry-After`. Devices already signed in
  are unaffected. Behind a reverse proxy, pass `--trust-proxy` (or
  `KITHARA_TRUST_PROXY=1`) so the limit applies per client, not to the proxy; leave it off
  otherwise, or clients could choose their own address.
- Audio at `/audio/{bookId}/{index}` and covers at `/covers/{bookId}` need the bearer
  token and support ranges and `HEAD`.

## Licence

MIT.
