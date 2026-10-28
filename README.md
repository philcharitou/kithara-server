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
kithara-server user add phil                  # asks for a password (8+ characters); the first user is the admin
kithara-server serve --library ~/Audiobooks --data ~/.kithara-server --name "Phil's shelf"
```

Then in Kithara: **Add a library > Kithara sync server**, address `http://your-machine:8080`,
your user name and password. The app checks `/api/health`, signs in, and syncs.

`kithara-server scan --library ~/Audiobooks` prints what the server sees without serving.

**The web page.** Open the server's address in a browser (`http://your-machine:8080`)
and sign in with the same account. Everyone can browse their libraries, see their
listening stats, download a book as a zip, and sign out their devices. Admins can also
rescan, find and edit metadata (with covers), and manage users and their library access.
It's part of the binary and uses the same API as the apps. For everyone:

- **Listen in the browser**: a player bar with chapters, ±30 s, speed, a sleep timer,
  bookmarks and media keys. Your place is saved like the apps save it, so you can carry on
  from your phone.
- The library as a table, as covers, or **by series and by author**; mark books finished or
  start them over; see, play from and delete bookmarks; download a book as a zip.
- Listening stats, your devices, and an **Account** page to change your own password.

For admins, also:

- **Libraries**: add folders from a picker, and **upload** audiobooks into them (drag in
  folders or files).
- **Maintenance**: files the last scan skipped and why, and books missing a cover, author,
  narrator or description, with **bulk matching** against Audible, Open Library or Google
  Books (review the matches, apply the ticked ones in one go).
- Per book: find or edit details, **change the cover** (upload a picture), **edit chapters**.
- **Activity**: who listened to what, and recent sessions. **Users**, and **Settings**: the
  server's name, how often it rescans (no restart needed), and a **backup** download of
  everything that can't be rebuilt from the audiobook folders.

Dark by default, in the apps' colours; the sun in the top bar switches to light.

**Adding libraries without restarting.** An admin can add a folder as a library from the
web page (the folder button in the Library tab's top bar, which opens a picker over the
server's own disk) or from the command line:

```bash
kithara-server library add Lectures /srv/talks
kithara-server library list
kithara-server library remove Lectures
```

These are kept in `libraries.json` in the data folder and picked up on the next scan.
Libraries given with `--library` stay as they are and can only be changed there. With
neither, the server starts with no libraries (or `./library` if there is one), ready for
an admin to add some. Removing a library takes its books off every device but leaves
the files, and everyone's places in them, alone: add the folder back and both return.

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
- Metadata and covers an admin has matched or edited (see below) override all of the
  above.
- Each book folder gets a tiny `.kithara-id` file so its id survives renames and moves
  and your listening position stays attached. If the folder is read-only, the id is kept
  in the data folder keyed by path instead (and a rename then looks like a new book).

Changed and new books are picked up every `--rescan` interval (default 10 minutes), on a
`POST /api/rescan` from an admin, and at start. Probing is cached by file size
and modification time, so rescans of a large unchanged library cost almost nothing.

## Users, roles and devices

The first user you add is an **admin**; later ones are plain **users** unless you say
otherwise. Admins can rescan, manage users and devices, and match or edit metadata.
Users listen. A user can also be limited to some libraries, and books outside them don't
exist as far as that user's devices are concerned. Admins always see everything.

```bash
kithara-server user add anna                        # a plain user who sees every library
kithara-server user add sam --libraries Kids        # sees only the Kids library
kithara-server user role anna admin                 # or back to: user
kithara-server user libraries sam Kids,Lectures     # or: all
kithara-server user list                            # names, roles, libraries, device counts
kithara-server user devices anna                    # where anna is signed in, and when last seen
kithara-server user signout anna 3f9a1c0b2d4e       # one device, by the id from `user devices`
kithara-server user signout anna                    # every device
```

These commands work on the data folder while the server is running (for example via
`docker exec`). The server picks the changes up on the next request, with no restart.
Users from a version before roles existed all become admins on upgrade, because back
then everyone could do everything. Demote any who shouldn't be. The last admin can't
be demoted or removed.

### Single sign-on

People can also sign in through an identity provider that speaks OpenID Connect
(Authelia, Authentik, Keycloak, Google and others). Register Kithara with the provider
as a web app (confidential, or public with PKCE), with the redirect address
`https://your.server/api/oidc/callback`, then fill in **Settings → Single sign-on** on
the web page: the issuer, the client id and secret, and the address browsers reach this
server at.

The first time someone signs in, they're matched to the account whose user name equals
the provider's `preferred_username` claim (or another claim you choose). From then on,
they're matched by the provider's own id for them, even if their name changes there.
Someone the provider knows but this server doesn't is turned away. If you tick
"make an account", they get a new plain account instead. Pick a claim the provider
controls, not one people can edit themselves.

## Metadata matching

An admin can look a book up with **Audible**, **Open Library** or **Google Books** and
keep the result:

```bash
kithara-server match 8f949203900f                     # list Audible's results (ids: `scan`)
kithara-server match --provider openlibrary 8f949203900f
kithara-server match --title "The Hobbit" --region uk 8f949203900f
kithara-server match --apply 1 8f949203900f           # keep the first result and its cover
kithara-server match --clear 8f949203900f             # forget it again
```

The server contacts these services only when an admin asks, for one book at a time. The
chosen metadata is saved in the data folder, never written into the library, so a
read-only library works. It overrides everything read from the library. Covers are only
fetched over https from the providers' image hosts. Google Books often answers
`429 Too Many Requests` without an API key; set `KITHARA_GOOGLE_BOOKS_KEY` to use one.

## Data folder

```
users.json            user names, roles, libraries and password hashes (PBKDF2-SHA256, 600k rounds)
tokens.json           device tokens, one per sign-in, with when each was last seen
users/<name>/progress.json
users/<name>/bookmarks.json
users/<name>/sessions.json  listening sessions, sent by the apps or worked out from progress
users/<name>/playlists.json
collections.json      collections, shared by everyone
oidc.json             the identity provider set-up, client secret included
probe-cache.json      ffprobe results, by file
covers/               artwork extracted from files, and covers chosen by matching
metadata/<bookId>.json  metadata an admin matched or edited (audiobookshelf's format)
ids-<library>.json    ids by path, for each library (ids.json in older versions)
libraries.json        libraries added from the web page or `kithara-server library add`
settings.json         server name and rescan interval set from the web page
```

Back up `users.json`, the `users/` folder, `collections.json`, `oidc.json` and
`metadata/` (plus `covers/*-match.*`), or use the backup button on the web page.
Everything else is rebuilt from the library.
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
- `POST /api/rescan` (admin) triggers a scan and returns the book count.
- `DELETE /api/progress/{bookId}` forgets a position (not used by the app; handy for
  scripts).
- Progress records also carry `startedAt` and `finishedAt`, kept by the server.
- `GET /api/health` lists optional `features`: `collections`, `playlists`,
  `reportedSessions` and `oidc`.
- `GET /api/sessions` (optionally `?bookId=` and `?since=` ms) lists listening sessions.
  An app can send its own with `POST /api/sessions`, as `{"sessions": [...]}` (each with
  the app's own `id`; sending an id again replaces it). The server then stops estimating
  for that device. For other devices it estimates. The apps upload progress when
  playback pauses, so the stretch between two uploads is counted as listening, and
  joined to the session before it if playback resumed within ten minutes. Jumps that
  playback could not have covered in the time (seeks, a book marked finished) count for
  nothing, so those figures lean low.
- **Collections and playlists:** `GET`/`POST /api/collections` and
  `PATCH`/`DELETE /api/collections/{id}`, with `name`, `description` and `bookIds`.
  Collections are shared, and only admins change them. Each person sees only the books
  in them they may see. `/api/playlists` works the same way for each user's own
  playlists.
- **Single sign-on:** `GET /api/oidc` says whether it is on. `GET /api/oidc/start` takes
  `?challenge=` (PKCE S256, made by the caller), `?redirect=kithara://…` for an app, and
  `?device=`. It goes through the provider and back, and ends at the page or app with a
  one-time `code`. `POST /api/oidc/exchange` with `code` and `verifier` returns a
  token, the same as `/api/auth`. Admins set it up with `GET`/`PUT /api/settings/oidc`;
  the secret is never sent back.
- `GET /api/stats?tzOffset=MINUTES` (minutes east of UTC; days begin at local midnight)
  sums those sessions: total time, time per day, per book, days listened, current and
  longest streak, books started and finished.
- `GET /api/libraries` and `GET /api/library?library=NAME`, as above. For admins each
  library also carries its `path`, `fixed` (set on the command line) and the `error`
  from its last scan, if any.
- **Libraries (admin):** `POST /api/libraries` with `path` (as the server sees it) and an
  optional `name` adds one and scans it; `DELETE /api/libraries/{name}` removes one added
  that way (`409` for one from the command line). `GET /api/fs?path=` lists the folders
  in a folder on the server, for picking one.
- Failed sign-ins are limited to 10 per address and 30 per user name in 15 minutes;
  past that, `POST /api/auth` answers `429` with `Retry-After`. Devices already signed in
  are unaffected, and so are addresses that have signed in to that account before: the
  per-account limit doesn't apply to them, so a stranger can't lock the owner out. Behind a reverse proxy, pass `--trust-proxy` (or
  `KITHARA_TRUST_PROXY=1`) so the limit applies per client, not to the proxy; leave it off
  otherwise, or clients could choose their own address.
- `POST /api/auth` also returns the user's `role`. `GET /api/me` returns the signed-in
  account (`name`, `role`, `libraries`, `devices`).
- **Devices:** `GET /api/devices` lists the caller's signed-in devices (`id`, `name`,
  `createdAt`, `lastSeenAt`, `current`); `DELETE /api/devices/{id}` signs one out. Admins
  can pass `?user=NAME` to act on someone else's devices. Tokens are never listed.
- **Users (admin):** `GET /api/users`; `POST /api/users` with `username`, `password` and
  optionally `role` and `libraries`; `PATCH /api/users/{name}` with any of `password`,
  `role` and `libraries` (`[]` means all libraries); `DELETE /api/users/{name}`.
- **Library access:** a user limited to some libraries gets `404` for books outside them
  on every route, and those books are left out of `/api/library` and `/api/libraries`.
  Admin-only routes answer `403` to plain users.
- **Download:** `GET /api/books/{bookId}/download` sends the whole book as a zip: its
  files under their paths in the book folder (so disc folders stay apart), plus its cover.
  `POST /api/books/{bookId}/download-link` returns a `url` for the same zip that needs no
  Authorization header for 10 minutes, for browsers and system download managers. Links
  are signed with a key held only in memory, so a restart voids them. So do a password
  change and signing out the device that asked for the link.
- **Streaming:** `POST /api/books/{bookId}/stream-link` returns signed `files[].url`s
  that play without an Authorization header for 12 hours (for an `<audio>` element).
  `/audio/...` and `/api/books/{id}/download` accept the same `?u=&d=&exp=&sig=` links.
- `PUT /api/me/password` with `current` and `new` changes your own password; every other
  device is signed out, and the response carries a fresh `token` for this one.
- **Admin:** `PUT /api/libraries/{name}/files?path=Author/Title/file.mp3` (raw body)
  uploads a file (audio, pictures, .opf/.json/.txt; `409` if it exists unless
  `&overwrite=1`); `PUT`/`DELETE /api/books/{id}/cover` (raw picture body);
  `PUT /api/books/{id}/chapters` with `chapters: [{title, startMs}]` (`[]` goes back to
  the files' chapters); `PUT /api/books/{id}/metadata?rescan=0` saves without rescanning,
  for batches; `GET /api/problems`; `GET`/`PUT /api/settings` (`name`,
  `rescanMinutes`; kept in `settings.json`, overriding `--name`/`--rescan`);
  `GET /api/backup` (a zip); `GET /api/activity?days=30`.
- How the apps can use all of this: [docs/app-integration.md](docs/app-integration.md).
- **Metadata (admin):** `GET /api/books/{bookId}/match?provider=audible|openlibrary|google`
  searches by the book's own title and author, or by `title`, `author`, `asin` (Audible)
  and `region` (Audible: us, uk, ca, au, de, fr, it, es, in, jp). Each result is a set of
  metadata fields plus `coverUrl`. `PUT /api/books/{bookId}/metadata` with a result,
  or with fields typed by hand, saves them (replacing whatever was saved before) and
  fetches `coverUrl` if one is given. A body with only `coverUrl` changes just the
  cover. `DELETE` on the same path forgets both. Either way the server rescans and
  answers with the updated book, whose `updatedAt` has moved so devices pick it up.
- Audio at `/audio/{bookId}/{index}` and covers at `/covers/{bookId}` need the bearer
  token and support ranges and `HEAD`.

## Licence

MIT.
