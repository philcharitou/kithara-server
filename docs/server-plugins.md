# Server plugins: design

Status: phase 1 (plugins in Settings) built, 19 November 2026; the rest is proposed.

## Goal

Let a server owner add **sources**: services, libraries and catalogues that can be searched
from the apps, and whose books can be brought into a library on the server. The phone
never runs plugin code. It asks its server for results and shows them in its own screens,
so the same feature works on iPhone, Android and the web page alike.

Kithara itself ships no plugins in the server image, and the apps know of none. A
reference plugin for public-domain audiobooks shows how one is written (see Phases).
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

POST /kithara/download    { "downloadId": "d_…", "id": "…" | "link": "…", "settings": {…} }
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
server's container, mounted into the plugin's as well. Each download gets
`/incoming/<downloadId>/`, which the plugin fills.

When the plugin reports `FINISHED`, the server:
1. Checks the files: audio, covers, `.cue`, `.nfo` and the metadata files the scanner
   already reads; no links, nothing outside the folder; within the size limit. Anything
   else is left out and listed on the download.
2. Moves them into the chosen library as `Author/Title/` (from the preview, cleaned for
   file names), or into the folder the user picked.
3. Rescans that library, tells the plugin to forget the download (`DELETE`), and removes
   the incoming folder.
4. Sends the `download.finished` webhook, or `download.failed` with the reason.

The book then appears on every device like any other, with its id kept from then on.
Auto-transcribe and metadata matching apply to it as to a book copied in by hand.

## What the apps see

A new feature in `/api/health`: `pluginSearch`. It says the server understands the
endpoints below. It says nothing about whether any plugin is installed: the apps show the
Search tab only when `/api/search/sources` lists at least one source.

```
GET    /api/search/sources
→ { "sources": [ { "id": "librivox", "name": "LibriVox", "description": "…",
                   "emptyQueryTitle": "…", "canDownload": true } ] }

GET    /api/search?source=librivox&q=dune&page=0
→ { "results": [ … ], "total": 120, "hasMore": true }       (as the plugin answered)

GET    /api/search/{source}/results/{id}                    the preview
GET    /api/search/{source}/cover?u=…                       covers, through the server

POST   /api/downloads   { "source": "librivox", "id": "…", "libraryId": "…" }
→ { "id": "d_…", "state": "QUEUED", … }
GET    /api/downloads                                       yours (an admin's: everyone's)
DELETE /api/downloads/{id}
```

The server passes results on as data and never as markup: text fields are plain text,
trimmed, and limited in length; links in results are not shown to the apps except covers,
which the server fetches and caches itself. That way a phone never contacts a plugin's
sources directly, and a plugin reachable only on the server's own network still works.

Progress reaches the apps the way the transcription queue does: `GET /api/downloads`
while a download is running. The server polls each running download every few seconds,
and only while one is running.

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
- A download can only go into a library the user can see, and a listener's downloads count
  against a limit the admin sets (how many at once).
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
4. **Reference plugin:** a public-domain audiobook source in its own small repository,
   with a Compose snippet, as the example for anyone writing one. Also a
   `docs/writing-a-server-plugin.md` with the contract above and a test harness.
5. **The apps:** the Search tab on iPhone and Android drawn from the server, beside the
   Android plugins' sources.

Phases 1 to 3 are the server; each leaves a working state. Rough size: phase 1 one session,
2 one, 3 two, 4 one.

## Open questions

1. Programs as plugins too (a folder of executables the server runs with JSON on stdin) for
   people who don't use Docker? It would be simpler for them, but it means running code in the
   server's container. Possibly later, behind a flag.
2. Should a plugin also be able to push events (`POST /api/plugins/{id}/events` with its
   secret) instead of being polled? Polling is enough at family scale.
3. Shared incoming folder for all plugins, or one per plugin? One per plugin keeps a
   misbehaving plugin out of the others' downloads, at the cost of one more mount each.
4. Should listeners see each other's downloads, as they see each other's books once they
   land?
