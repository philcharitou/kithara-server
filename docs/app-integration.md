# Using kithara-server 0.2 from the apps

What the Android and iOS clients need to do to use what kithara-server 0.2.0 added.
Everything here **extends** protocol version 1: `/api/health` still says `protocol: 1`,
and a client that does none of it keeps working exactly as before. All of it is
optional, so detect it rather than assume it: a route that answers `404` means an
older or different server, and the feature should stay hidden.

Every call below is bearer-authenticated, like the rest of the protocol. Times are
epoch milliseconds, as in the protocol.

The server's README lists every route; this document covers what is worth building
and in what order.

---

## 1. Small fixes with a big effect (do these first)

These are gaps in the current clients (see §10 and §11 of the protocol document), and
the new server features depend on them.

| What | Why it matters now |
|---|---|
| **Send `device` on `POST /api/auth`** (Android doesn't; iOS does). Use the device's user-visible name, e.g. "Pixel 9". | It is the name shown in the device list on the web page and in `GET /api/devices`. Without it, devices show as "Unnamed" and nobody can tell which to sign out. |
| **Send `speed` on `PUT /api/progress`** whenever it isn't 1.0 (neither app does). | The server estimates listening time from the gap between two progress uploads. At 2x, a 40-minute jump in position is 20 minutes of listening. Without `speed` the server assumes 1x, so stats come out too high for fast listeners. |
| **Treat `429` on `POST /api/auth` as "too many tries"**, and show `Retry-After` (seconds) if you like. | Sign-in is now limited to 10 failures per address and 30 per account in 15 minutes. The protocol already says 429 means retry later; the sign-in screen just needs a sensible message instead of a generic error. |
| **Treat `403` as "not allowed", not "sign in again"**. | Admin-only routes answer `403` to plain users. Only `401` means the token is dead. |

## 2. New fields on books (cheap, visible)

`GET /api/library` books can now carry these extra fields, each omitted when unknown:

```json
{
  "library": "Kids",
  "subtitle": "A Novel",
  "genres": ["Literary", "Fiction"],
  "publisher": "Tidewater",
  "publishedYear": "2019",
  "language": "english",
  "isbn": "9781234567897",
  "asin": "B0STAR"
}
```

`author`, `narrator`, `series`, `seriesIndex` and `description` are now filled far more
often too: from folder names (`Author/Series/3 - Title {Narrator}`), `.opf`,
`desc.txt`/`reader.txt`, audiobookshelf's `metadata.json`, and metadata an admin has
matched on the server.

- Store the new fields and show them on the book screen: subtitle under the title,
  genres as chips, and year · publisher.
- **Sort and group by series** using `series` + `seriesIndex`. These are much more
  reliable now.
- A book's `updatedAt` moves whenever its metadata or cover changes, including when an
  admin matches it, so the existing "skip unchanged books" logic already picks those
  changes up. Make sure the cover is fetched again when `updatedAt` changes (the
  protocol already says to).

## 3. Libraries (one server, several shelves)

The server can serve several folders as named libraries. The protocol is unchanged:
`GET /api/library` still returns **one** authoritative list, and each book now says
which library it belongs to.

```
GET /api/libraries
{ "libraries": [ { "name": "Audiobooks", "books": 412, "durationMs": 4.1e9, "updatedAt": 1737000000000 },
                 { "name": "Kids",       "books": 38,  "durationMs": 1.2e8, "updatedAt": 1736000000000 } ] }
```

- If there's more than one, offer a **library filter** (chips or a menu) over the books
  you already have, using `book.library`. No extra request is needed.
  `GET /api/library?library=Kids` also exists, but **don't use it for sync**: it
  returns only that library's books, and the sync code would then treat every other
  book as deleted. Keep full syncs on plain `/api/library`.
- A user can be limited to some libraries. Their `/api/library` and `/api/libraries`
  simply leave the rest out, and those books answer `404` everywhere. The existing
  "book gone → drop it" handling already covers someone losing access.

## 4. Who am I, and devices

```
GET /api/me
{ "name": "phil", "role": "admin", "libraries": [], "devices": 3 }
```

`POST /api/auth` also returns `role` now. `role` is `"admin"` or `"user"`. Use it only to
show or hide admin features; the server enforces access regardless.

```
GET /api/devices
{ "devices": [ { "id": "3f9a1c0b2d4e", "name": "Pixel 9", "createdAt": …, "lastSeenAt": …, "current": true }, … ] }

DELETE /api/devices/{id}      → 204, or 404 if already gone
```

- Add a **"Signed-in devices"** screen in the server's settings: the list, with a
  sign-out action on each. Mark the `current` one; signing it out is the same as
  removing the library.
- `lastSeenAt` is only updated about once an hour, so show it as "today",
  "yesterday", "3 days ago" rather than a time.
- Admins can add `?user=NAME` to both routes. That's for the web page; the apps don't
  need it.

## 5. Listening stats

The server works out listening sessions from progress uploads, so this works with the
apps as they are today (and better once they send `speed`, see §1).

```
GET /api/stats?tzOffset=60          (minutes EAST of UTC, i.e. -Date.getTimezoneOffset())
{
  "totalListenedMs": 123456789,
  "daysListened": 41, "currentStreakDays": 6, "longestStreakDays": 19,
  "booksStarted": 22, "booksFinished": 14,
  "days":  [ { "date": "2026-10-27", "listenedMs": 3600000 }, … ],      // only days with listening
  "books": [ { "bookId": "…", "listenedMs": …, "lastListenedAt": …, "finishedAt": … }, … ]   // most recent first
}

GET /api/sessions?bookId=…&since=…
{ "sessions": [ { "id": "ls_…", "bookId": "…", "device": "Pixel 9", "startedAt": …, "endedAt": …,
                  "startPositionMs": …, "endPositionMs": …, "listenedMs": … } ] }
```

Progress records also carry `startedAt` and `finishedAt` now. The server maintains them;
clients don't need to send them.

- If the app already has a stats screen built from on-device data, show the **server's
  figures for server libraries**, so they cover every device. Label them as an
  estimate. The protocol keeps on-device stats as they are, so don't replace those.
- `finishedAt` gives a reliable "Finished on …" and a "finished this year" count.
- Compute `tzOffset` on every request (it changes with daylight saving).

## 6. Download a whole book

For "save to Files" / "share" / "download in the background with the system
downloader", where sending a bearer header is awkward:

```
POST /api/books/{bookId}/download-link
{ "url": "/api/books/abc/download?u=phil&exp=1793202478&sig=…", "expiresAt": 1793202478000 }
```

The `url` (relative to the server root) needs **no** Authorization header and works
for 10 minutes. It returns a zip: the book's files under their folder paths
(`CD 1/track 01.m4b`), plus `cover.jpg`/`.png`, stored uncompressed. Use it with
Android's `DownloadManager` or iOS's background `URLSession`. Don't keep the URL;
ask for a new one each time.

`GET /api/books/{bookId}/download` with the bearer header returns the same zip if
that's simpler. Offline playback inside the app should keep using the per-file
`/audio/…` URLs, as now; the zip is for taking a book out of the app.

## 7. Admin features (optional in the apps)

All of these work from the server's own web page (open the server address in a
browser), so the apps don't need them. If you want them anyway, only for `role ==
"admin"`:

- `POST /api/rescan` → `{ "books": N }`. "Refresh library now". Plain users get `403`.
- `GET /api/books/{id}/match?provider=audible|openlibrary|google[&title=&author=&region=us|uk|…]`
  → `{ "results": [ { "title", "subtitle", "authors": [], "narrators": [], "series",
  "seriesIndex", "description", "publisher", "publishedYear", "language", "isbn",
  "asin", "genres": [], "coverUrl", "durationMs", "provider" } ] }`
- `PUT /api/books/{id}/metadata` with one of those results (or hand-typed fields) saves
  it and fetches `coverUrl`. It answers with the updated book.
  `DELETE /api/books/{id}/metadata` undoes it.
- `GET/POST /api/users`, `PATCH/DELETE /api/users/{name}` for user management.

## 8. Protocol document

`docs/kithara-sync-protocol.md` (Android repo) and the site copy still describe
version 1 as it was. Suggested edits, whoever owns that document:

- §8 lists "Multiple libraries per server" and "Listening statistics" as left out of
  version 1. Both now exist **as optional extensions**: one catalogue with a
  `library` field, and server-estimated sessions. Mention them as a server MAY, with
  the routes above.
- Add the new optional book fields from §2 above to §4 of that document.
- The conformance tables (§10/§11) gain rows for: `device` sent, `speed` sent, `429`
  on auth, libraries filter, devices screen, stats, download link.

A **version 2** candidate, for when it's worth it: clients upload their own listening
sessions (`POST /api/sessions`, idempotent on a client-generated id). The server would
then stop estimating for those clients, and the stats become exact. Nothing in 0.2.0
depends on this.

## Suggested order

1. §1, all four items. Small, and they make everything else accurate.
2. §2 (new fields) and §3 (library filter).
3. §4 (devices screen) and §6 (download).
4. §5 (stats).
5. §7 only if admins want it in the app rather than on the web page.
