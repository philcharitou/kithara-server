# Server plugins: design

Status: phases 1 to 4 (plugins in Settings, search, downloads, the example plugins and the guide) built, 27 November 2026; the apps are proposed. How to write one: [writing-a-server-plugin.md](writing-a-server-plugin.md).

## Goal

Let a server owner add **sources**: services, libraries and catalogues that can be searched
from the apps, and whose books can be brought into a library on the server. The phone
never runs plugin code. It asks its server for results and shows them in its own screens,
so the same feature works on iPhone, Android and the web page alike.

Kithara itself ships no plugins in the server image, and the apps know of none. Two
example plugins in `plugins/` show how one is written: LibriVox's public-domain
audiobooks (with its own image, `kithara-plugin-librivox`), and a one-file Python plugin
for a list of files.
Anyone can write a plugin for their own server; what a server's owner installs there is
their business, as with any software they run on their own machine.

## Relation to the Android plugins

The Android app has its own plugin system (`Kithara-Android/docs/plugin-design.md`): a
plugin is an APK, and Kithara talks to it over Android's inter-app channels. That works
only on Android, and only for the phone it is installed on. Server plugins are the same
idea one level up:

| | Android plugin | Server plugin |
|---|---|---|
| Runs on | the phone | the server, or beside it |
| Reaches | that phone | every app and the web page signed in to the server |
| Installed by | the phone's owner | the server's admin |
| Results drawn by | Kithara | Kithara (each app, from the server's data) |
| Downloads land in | the phone's storage, then a library folder | a library folder on the server |

The data shapes (source, result, preview, download, hand-off) are deliberately the
Android plugin API's, so an app can show sources from both in one Search tab with the same
code.

## The shape of a plugin

**A plugin is a small HTTP service.** The admin adds it in Settings → Plugins with its
address and a secret. It might be a container beside the server in the same Compose file
(`http://my-plugin:9000`), or a program on the same machine (`http://127.0.0.1:9000`).

Why HTTP, and not programs the server starts:
- Any language works, and the plugin brings its own runtime. The server image stays the
  small Go binary it is, and the server never runs code in its own process.
- It matches what is already there: webhooks are HTTP, and Docker users already think in
  containers.
- A plugin can be updated, restarted or removed without touching the server.

The server calls the plugin; the plugin never needs the server's address or a user's
token. Every call carries `Authorization: Bearer <secret>`, the secret the admin pasted
into both. Every call has a timeout (search 10 s, everything else 30 s), and every answer
is size-limited (1 MB).

### What the plugin answers

All bodies are JSON. Field names follow the Android plugin API.

```
GET  /kithara/plugin
→ { "name": "Public domain audiobooks", "version": "1.0.0", "apiVersion": 1,
    "search": { "name": "LibriVox", "description": "…", "emptyQueryTitle": "Recently added" },
    "downloads": true,                     // it can bring books in
    "accepts": ["…"],                      // kinds of link it can download (hand-offs, below)
    "settings": [ { "id": "language", "title": "Language", "type": "choice",
                    "choices": ["English", "French"], "default": "English", "hint": "…" } ] }

POST /kithara/search      { "query": "dune", "page": 0, "settings": {…} }
→ { "results": [ { "id", "title", "author", "narrator", "cover", "year" } ],
    "total": 120, "hasMore": true }

POST /kithara/preview     { "id": "…", "settings": {…} }
→ { "id", "title", "author", "narrator", "cover", "description", "lengthSeconds",
    "sizeBytes", "files", "format", "language" }

POST /kithara/download    { "downloadId": "d_…", "folder": "d_…", "id": "…" | "link": "…", "settings": {…} }
→ { "ok": true }  or  { "ok": false, "error": "…" }  or  { "ok": true, "open": "<link>" }

GET  /kithara/downloads/{downloadId}
→ { "state": "QUEUED|DOWNLOADING|PAUSED|FINISHED|FAILED", "progress": 0.42,
    "bytesPerSecond": 1200000, "sizeBytes": 314572800, "error": null }

DELETE /kithara/downloads/{downloadId}            cancel, and forget its files
```

A plugin may offer search only, downloads only, or both; it must offer one. `apiVersion`
is the newest it speaks: a newer one than the server's is talked to in the server's.

`settings` describe the plugin's form in Settings, up to 20 fields of type `text`,
`secret`, `number`, `toggle` (`"true"`/`"false"`) or `choice`. The admin's answers (an
account, a language, a region) are kept in `plugins.json` and sent with each call, so the
plugin can be stateless. Answers to `secret` fields, like the plugin's own secret, are never
sent back to the web page.

Names are trimmed (80 characters, descriptions 300), and kinds of link are lower-case
letters, digits and `. + -`. The server checks a plugin when it is added, when its address
or secret changes, on Check, and every 15 minutes, and shows in Settings why one is not
answering.

### Hand-offs

A source may find books it cannot fetch itself. Its `/kithara/download` then answers
`open` with a link. The server gives that link to a plugin whose `accepts` lists the
link's kind (the part before `:`, or a file extension), and tracks that plugin's download
against the original result. Sources and downloaders stay separate plugins that know
nothing of each other; the server knows no kinds of its own.

### Where downloads go

The server owns the library folders and is the only one that writes into them, as on
Android. Plugins write into a shared **incoming** folder instead: `/incoming` in the
server's image (`KITHARA_INCOMING`; without it, `incoming` in the data folder), mounted
into the plugin's container as well:

```yaml
services:
  kithara:
    volumes: [incoming:/incoming, …]
  my-plugin:
    image: …
    volumes: [incoming:/incoming]
volumes:
  incoming:
```

Before asking the plugin, the server makes `/incoming/<folder>/` (the `folder` it sends,
which is the download's id), writable by anyone, so a plugin running as another user can
fill it. Folders inside it should be writable by the server too (run the plugin as user
1000, or with umask 000), or the server copies instead of moving and can't tidy up.

When the plugin reports `FINISHED`, the server (removing nothing in the incoming folder
but the `d_…` folders it made itself):
1. Checks the files: audio, pictures, `.cue`, `.nfo`, `.opf`, `.txt` and `.json` (the files
   the scanner reads), at most two folders deep, 50 GB in all. Links, hidden files and
   anything else are left out and listed on the download (`skipped`). No audio at all
   fails it.
2. Moves them into the chosen library as `Author/Title/` (the result's names, made safe
   for any file system; `Title (2)` when that is taken). Unless the download brought a
   `metadata.json` or an `.opf` of its own, it writes one with the result's title, author,
   narrator, year, language and description, so the book is named as the user saw it
   whatever the files' tags say. The files gather first in a hidden folder in the
   library (`.kithara-incoming-<id>`, which scans skip), which then becomes the book's
   folder in one rename: no scan sees half a book, and a server restarted in the middle
   carries on.
3. Rescans, notes the new book's id on the download (`bookId`), tells the plugin to forget
   it (`DELETE`), and removes the incoming folder.
4. Sends the `download.finished` webhook, or `download.failed` with the reason.

The plugin's `GET /kithara/downloads/{id}` is asked every 3 seconds while a download is
running, and not at all otherwise. A plugin's `FAILED` (with its `error`) fails the
download and the plugin is told to forget it.

The book then appears on every device like any other, with its id kept from then on.
Auto-transcribe and metadata matching apply to it as to a book copied in by hand.

## What the apps see

A new feature in `/api/health`: `pluginSearch` (built). It says the server understands the
search endpoints below. It says nothing about whether any plugin is installed: the apps show
the Search tab only when `/api/search/sources` lists at least one source. The downloads
endpoints come with phase 3.

```
GET    /api/search/sources
→ { "sources": [ { "id": "3d17deedff18", "name": "LibriVox", "description": "…",
                   "emptyQueryTitle": "…", "canDownload": true } ] }

GET    /api/search?source=3d17deedff18&q=dune&page=0
→ { "results": [ { "id", "title", "author", "narrator", "cover", "year" } ],
    "total": 120, "hasMore": true }                         (total -1: the source doesn't say)

GET    /api/search/{source}/results/{id}                    the preview (id path-escaped)
GET    /api/search/{source}/cover?u=…&sig=…                 a result's cover, as given

POST   /api/downloads   { "source": "librivox", "id": "…", "libraryId": "…" }
→ { "id": "d_…", "state": "QUEUED", … }
GET    /api/downloads                                       yours (an admin's: everyone's)
DELETE /api/downloads/{id}
```

A source's id is the plugin's. Sources are listed by name, and only those the user may
search whose plugin answered its last check. An empty `q` asks for what the source shows
first, under its `emptyQueryTitle`. A source that fails answers `502` with
"NAME didn't answer: why"; one the user may not search, `404`.

The server passes results on as data and never as markup: text fields are plain text
(tags, script and style dropped, entities decoded, one line; a preview's description keeps
its paragraphs, separated by a blank line), trimmed, and limited in length (titles 300,
names 200, descriptions 5,000 characters). Results without an id or a title are dropped,
and a page holds at most 100. Links in results are not shown to the apps except covers:
an http(s) cover becomes an address on this server, signed with a key the server keeps in
memory, and only signed addresses are fetched (a picture, at most 5 MB, kept for a month
in the data folder's `cache`). So a phone never contacts a plugin's sources directly, a
signed-in user cannot make the server fetch anything else, and a plugin reachable only on
the server's own network still works. A cover's address must not lead to the server's
machine or the local network (loopback, private, link-local or Tailscale addresses),
except to the plugin's own address: a source's results can't make the server knock on a
router or a cloud's metadata service. The check is on the address actually connected to,
redirects included. Cover addresses stop working when the server restarts; search again
for new ones.

Each call to a plugin carries `settings` (the admin's answers over the plugin's defaults)
and, if the admin turned it on, `user`. Search and preview calls time out after 10
seconds, and the server makes at most 16 plugin calls at once.

`POST /api/downloads` takes `{source, id, library}` (the library's name) and answers
`201` with the download, `403` when the user may not download from that source, `400` for
a library they can't see, `429` when a listener already has 3 running, and `502` when the
source (or a hand-off's plugin) refuses, with its reason. A download is:

```
{ "id": "d_…", "user", "source", "plugin", "resultId", "title", "author", "narrator",
  "year", "language", "library",
  "state": "QUEUED|DOWNLOADING|PAUSED|MOVING|FINISHED|FAILED", "progress": 0.42,
  "bytesPerSecond", "sizeBytes", "error", "unavailable", "skipped": […], "bookId",
  "createdAt", "updatedAt" }
```

`MOVING` is the server moving the files in; `FINISHED` means the book is in the library
(`bookId`); `unavailable` means the plugin isn't answering just now. Progress reaches the
apps the way the transcription queue does: ask `GET /api/downloads` every few seconds
while one is running. `DELETE /api/downloads/{id}` stops a running one (the plugin is told
to forget it) or takes one that is over off the list; one that is `MOVING` can't be. The
server keeps the last 200.

## Who may do what

Admins manage plugins at `/api/plugins` (built): `GET` lists them with what each said
about itself and why one is not answering, `POST` (`url`, `secret`) adds one if it answers
as a plugin, `PATCH /api/plugins/{id}` changes `url`, `secret`, `enabled`, `search`,
`download`, `sendUser` and `settings`, `POST /api/plugins/{id}/check` asks it again, and
`DELETE` removes it. `plugins.json` is in the backups.

- **Admins** add, configure and remove plugins. A plugin's settings and secret are never
  sent to the apps.
- **Per plugin,** the admin chooses who may search it (everyone, or admins only) and who
  may download from it (admins only by default, or everyone).
- A download can only go into a library the user can see, and a listener has at most 3
  running at once. A hand-off goes only to a plugin the user may download from.
- The server tells a plugin which user is asking only if the admin turns that on for the
  plugin, for a plugin that keeps per-person accounts. Otherwise plugins see no names.

## Failure handling

- A plugin that is down or slow: its source shows as unavailable in Settings, drops out of
  `/api/search/sources`, and its running downloads show "Plugin unavailable" until it is
  back. Nothing already in a library is affected.
- The server restarts: running downloads are picked up again from `downloads.json` and the
  plugin's own state.
- A download whose files fail the checks: `FAILED`, with what was wrong, and its incoming
  folder kept for a day so the admin can look.
- A plugin removed while one of its downloads runs: that download fails.
- A plugin with a newer `apiVersion` than the server: the server uses its own version and
  ignores what it does not know. Older: the server hides what that version lacks.

## The App Store and Google Play

The apps' part is generic, and stays so:
- The apps say "Search" and show the sources the user's server offers, by the names the
  server gives. No source is built into the apps, named in their listings, screenshots or
  strings, or suggested by them, and the apps contain nothing that only makes sense for one
  kind of source.
- The apps behave the same for an App Review account as for anyone: they show what the
  server they are pointed at offers. Nothing is hidden or switched on later.
- The apps never download, link to or recommend plugins. Plugins are installed by a
  server's admin, on their own server.
- The reference plugin and the docs use public-domain sources.
- What a plugin does is the responsibility of whoever wrote it and whoever installed it.

## Phases

1. **Plugins on the server:** Settings → Plugins (add by address and secret, check
   `/kithara/plugin`, settings form, who may search and download), `plugins.json`, the
   calls with their secret, timeouts and size limits.
2. **Search:** `/api/search*`, the cover proxy, `pluginSearch`, and a Search page in the web
   interface, which proves the API before the apps use it.
3. **Downloads:** the incoming folder, `downloads.json`, polling, the checks and the move
   into a library, rescans, the webhooks, the downloads list on the web page, hand-offs.
4. **Example plugins** (built): `plugins/librivox`, LibriVox through the Internet Archive,
   in Go, with an image released beside the server's; `plugins/catalogue-python`, the
   smallest complete plugin; `plugins/plugincheck`, which calls a plugin as the server does
   and says what is wrong; and `docs/writing-a-server-plugin.md`. They live in this
   repository (each its own module) rather than in repositories of their own, so they
   change with the contract.
5. **The apps:** the Search tab on iPhone and Android drawn from the server, beside the
   Android plugins' sources.

Phases 1 to 4 are built; each leaves a working state.

## Open questions

1. Programs as plugins too (a folder of executables the server runs with JSON on stdin) for
   people who don't use Docker? It would be simpler for them, but it means running code in the
   server's container. Possibly later, behind a flag.
2. Should a plugin also be able to push events (`POST /api/plugins/{id}/events` with its
   secret) instead of being polled? Polling is enough at family scale.
3. Shared incoming folder for all plugins, or one per plugin? One per plugin keeps a
   misbehaving plugin out of the others' downloads, at the cost of one more mount each.
4. Should listeners see each other's downloads, as they see each other's books once they
   land? (Today each sees their own; admins see everyone's.)
5. Should the limit of 3 running downloads per listener be a setting?
