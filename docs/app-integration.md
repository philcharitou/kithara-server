# Using kithara-server's extensions from the apps

What the Android and iOS clients need to do to use what kithara-server has added on
top of the sync protocol, from 0.2 up to the current main branch. Everything here
**extends** protocol version 1: `/api/health` still says `protocol: 1`, and a client
that does none of it keeps working exactly as before.

Detect features rather than assume them. `/api/health` now lists them:

```json
{ "ok": true, "protocol": 1, "name": "Phil's shelf", "version": "0.6.0",
  "features": ["collections", "playlists", "reportedSessions", "oidc", "transcripts", "transcription"] }
```

Anything older has no `features`. In that case, a route that answers `404` means the
server doesn't have it, and the feature should stay hidden.

Every call below is bearer-authenticated unless it says otherwise. Times are epoch
milliseconds, as in the protocol. The server's README lists every route; this document
covers what is worth building and in what order. The checklist for each app is at the
end.

---

## 1. Small fixes with a big effect (do these first)

| What | Why it matters |
|---|---|
| **Send `device` on `POST /api/auth`.** Use the user-visible device name, e.g. "Pixel 9". | It names the device in the devices list and in listening sessions. Without it, devices show as "Unnamed" and nobody can tell which one to sign out. Names are cut to 64 characters. |
| **Send `speed` on `PUT /api/progress`** whenever it isn't 1.0. | Until an app reports its own sessions (§6), the server estimates listening time from the gap between two uploads. At 2x, a 40-minute jump is 20 minutes of listening. Without `speed`, the server can't tell a fast listener from a seek. |
| **Treat `429` and `503` on `POST /api/auth` as "try again later".** `429` comes with `Retry-After` in seconds. | Sign-in allows 10 failures per address and 30 per account in 15 minutes. `503` means too many password checks are already running. Neither one means the password is wrong. |
| **Treat `403` as "not allowed", not "sign in again".** | Admin-only routes answer `403` to plain users, and so does an expired signed link. Only `401` means the token is dead. |

## 2. New fields on books

`GET /api/library` books can carry these extra fields, each left out when unknown:

```json
{ "library": "Kids", "subtitle": "A Novel", "description": "…", "genres": ["Literary"],
  "publisher": "Tidewater", "publishedYear": "2019", "language": "english",
  "isbn": "9781234567897", "asin": "B0STAR" }
```

Files can also carry `mimeType`.

- Store the new fields and show them on the book screen: the subtitle under the title,
  genres as chips, and the year and publisher.
- `author`, `narrator`, `series` and `seriesIndex` are filled much more often now, from
  folder names, `.opf`, `desc.txt`, audiobookshelf's `metadata.json`, and admin edits.
  Sorting and grouping by series is worth adding.
- A book's `updatedAt` moves whenever an admin changes its details, cover or chapters.
  The existing "skip unchanged books" logic already picks that up. Fetch the cover again
  when `updatedAt` changes.
- The catalogue is sent gzipped, with `ETag` and `Vary: Accept-Encoding`. Both platforms'
  HTTP stacks decompress it on their own. Send `If-None-Match` to get `304` when nothing
  has changed.

## 3. Libraries

One server can serve several folders as named libraries. `GET /api/library` still
returns **one** authoritative list, and each book says which library it belongs to.

```
GET /api/libraries
{ "libraries": [ { "name": "Audiobooks", "books": 412, "durationMs": …, "updatedAt": … }, … ] }
```

- When there is more than one library, offer a filter over the books you already have,
  using `book.library`.
- `GET /api/library?library=Kids` also exists, but **don't use it for sync**: the sync
  code would treat every other book as deleted.
- A user can be limited to some libraries. The books outside them simply don't exist
  for that user and answer `404`. The existing "book gone → drop it" handling covers
  this.

## 4. Account and devices

```
GET /api/me                  { "name": "phil", "role": "admin", "libraries": [], "devices": 3 }
GET /api/devices             { "devices": [ { "id", "name", "createdAt", "lastSeenAt", "current" }, … ] }
DELETE /api/devices/{id}     → 204, or 404 if it is already gone
PUT /api/me/password         { "current": "…", "new": "…" }  → { "token": "…" }
```

- `POST /api/auth` returns `role` (`"admin"` or `"user"`). Use it only to show or hide
  admin features; the server enforces access regardless.
- Add a **"Signed-in devices"** screen to the server's settings, with sign-out on each
  device and the `current` one marked. `lastSeenAt` is updated about once an hour, so
  show it as "today" or "3 days ago".
- **Changing the password** signs out every other device. The answer carries a new
  token for this device, so **replace the stored token**. The route is rate-limited like
  sign-in, so it can answer `429` or `503`. A wrong current password gets `403`. A new
  password needs at least 8 characters.

## 5. Signed links: downloads and streaming without a header

For anything that can't send a bearer header (the system download manager, a cast
target, a player that wants a plain URL):

```
POST /api/books/{bookId}/download-link
{ "url": "/api/books/abc/download?u=…&d=…&exp=…&sig=…", "expiresAt": … }      // the zip, for 10 minutes

POST /api/books/{bookId}/stream-link
{ "files": [ { "index": 0, "url": "/audio/abc/0?u=…&d=…&exp=…&sig=…", "durationMs": … } ], "expiresAt": … }   // 12 hours
```

URLs are relative to the server root and need no Authorization header. A link is good
for one book and one purpose, so an audio link won't open the zip. It stops working
when:

- it expires,
- the user's password changes,
- **the device that asked for it is signed out**, or
- the server restarts.

Then it answers `403`. Ask for a new link each time rather than storing one. Offline
playback inside the app should keep using the per-file `/audio/…` URLs with the bearer
header, as now.

## 6. Listening sessions and stats

### Reporting sessions (do this if the app keeps its own listening log)

```
POST /api/sessions
{ "sessions": [ { "id": "8c1f…", "bookId": "…", "startedAt": …, "endedAt": …,
                  "startPositionMs": …, "endPositionMs": …, "listenedMs": … } ] }
→ { "stored": 1 }
```

- `id` is the app's own: 1 to 64 characters from `A–Z a–z 0–9 - _`. Sending the same
  id again from the same device **replaces** the stored session, so upload a session as
  it grows (on each pause, say) without making duplicates.
- `listenedMs` is wall-clock listening, so an hour at 2x is 30 minutes. It can't be more
  than `endedAt − startedAt`. A session lasts at most a day and can't end in the future.
  Send up to 500 at a time. If one is invalid, the whole batch is refused with `400`
  and the session's id in the message.
- From the first report on, **the server stops estimating sessions for that device**.
  Its progress uploads still sync position, but they no longer add sessions. Report
  every session from then on, including the backlog if you have it.

### Reading them

```
GET /api/stats?tzOffset=60          (minutes EAST of UTC, i.e. -Date.getTimezoneOffset())
{ "totalListenedMs", "daysListened", "currentStreakDays", "longestStreakDays",
  "booksStarted", "booksFinished", "days": [ { "date", "listenedMs" } ], "books": [ … ] }

GET /api/sessions?bookId=…&since=…
{ "sessions": [ { "id", "bookId", "device", "startedAt", "endedAt", "startPositionMs", "endPositionMs", "listenedMs" } ] }
```

- On a server library, show the **server's figures**, which cover every device. Until
  the app reports its own sessions, label them as an estimate.
- Progress records carry `startedAt` and `finishedAt`, which the server maintains.
  `finishedAt` gives "Finished on …".
- Compute `tzOffset` on every request, since it changes with daylight saving.

## 7. Collections and playlists

Named, ordered lists of books. **Collections** are shared: admins make them and
everyone sees them. **Playlists** belong to one user, and nobody else sees them.

```
GET    /api/collections            { "collections": [ { "id", "name", "description", "bookIds": [ … ], "updatedAt" } ] }
GET    /api/playlists              { "playlists":   [ … ] }
POST   /api/{collections|playlists}         { "name", "description", "bookIds" }  → 201 with the list
PATCH  /api/{collections|playlists}/{id}    any of those fields                   → the list
DELETE /api/{collections|playlists}/{id}    → 204
```

- Each person sees only the books in a collection that they're allowed to see. A
  collection with none of those isn't shown to them at all.
- Only admins may change collections; anyone else gets `403`. Anyone may change their
  own playlists.
- Every book in `bookIds` must exist and be visible to the caller, or the request gets
  `400`. A book given twice is kept once. Names are up to 100 characters. A list holds
  up to 2,000 books, and a user up to 200 lists.
- `PATCH` with `bookIds` replaces the whole order. To add a book, send the list with
  the book appended.
- Suggested UI: a "Collections" shelf on the library screen, and "Add to playlist" on
  the book screen. A playlist can also serve as an up-next queue: play the next book
  when one finishes.

## 8. Sign-in through an identity provider (single sign-on)

When the admin has set it up, people can sign in through Authelia, Authentik,
Keycloak, Google and the like, instead of typing a password into the app.

```
GET /api/oidc         (no auth)   { "enabled": true, "buttonText": "Sign in with Authelia" }
```

When `enabled` is true, show the button. The flow is OAuth for native apps, with PKCE
between the app and **this server**. The server does its own PKCE with the provider.

1. Make a random `verifier` (43 to 128 characters) and its
   `challenge = base64url(sha256(verifier))`, with no padding (43 characters).
2. Open the system browser (`ASWebAuthenticationSession` on iOS, a Custom Tab on
   Android) at
   `{server}/api/oidc/start?challenge=…&redirect=kithara://oidc&device=Pixel%209`.
   `redirect` must use the `kithara:` scheme.
3. The person signs in at the provider. The browser comes back to
   `kithara://oidc?code=…`, or `kithara://oidc?error=…` with a message to show.
4. `POST /api/oidc/exchange` (no auth) with `{ "code", "verifier", "device" }` returns
   `{ "token", "user", "role" }`, the same as `POST /api/auth`. The code works once,
   within 2 minutes, and only with the matching verifier. Otherwise the answer is `401`.

Register the `kithara` URL scheme on both platforms. Accounts are matched by the
provider's subject after the first sign-in. If the provider doesn't know someone,
they get the error "there is no account here for you" unless the admin turned on
automatic accounts.

## 9. Transcripts

The server keeps transcripts per book, shared by everyone who can see the book. With
whisper.cpp installed (`transcription` in `features`), it makes them itself, much faster
than a phone. It stores them in the same shape the app does:

```
GET /api/books/{bookId}/transcript
{ "bookId": "…",
  "chapters": [ { "index": 0, "status": "DONE", "model": "small.en", "engine": "whisper.cpp", "updatedAt": … } ],
  "passages": [ { "chapterIndex": 0, "startMs": 1200, "endMs": 5400, "text": "…" } ] }
```

- `passages` map one-to-one onto the app's `TranscriptSegmentEntity`, with times on the
  book's timeline. `chapters` map onto `TranscriptChapterEntity`: the same `DONE` and
  `PARTIAL`, and model ids like the app's (`tiny.en`, `base.en`, `small.en`, plus the
  server's bigger ones). It is gzipped for clients that accept it.
- **Asking the server to transcribe:** `POST /api/books/{bookId}/transcribe` with
  `{}` (every chapter not yet done) or `{"chapters": [3]}`. `DELETE` on the same path
  stops it. `GET /api/transcription` shows `running` (`bookId`, `chapter`, `progress`
  from 0 to 1) and `queue`. Poll it every few seconds while the book's transcript
  screen is open.
- **Sending up a phone's transcript:** `PUT /api/books/{bookId}/transcript/chapters/{index}`
  with `{ "status", "model", "engine": "sherpa-onnx", "passages": [ { "startMs", "endMs", "text" } ] }`.
  It replaces that chapter's passages. A chapter that is already `DONE` on the server
  answers `409` (only an admin may replace it), so check `chapters` first and don't
  upload over a finished one.
- `GET /api/transcripts/search?q=…&bookId=…` searches on the server. The app can keep
  searching locally.

**Pro.** Transcripts are a Pro feature in the app, and the server doesn't check
anything: it is the user's own machine. Gate it in the app. Without Pro, don't fetch,
show, upload or request transcripts, and hide the transcript screen for server books.
The web page offers transcripts to everyone, and that is intended: the server is free,
and Pro is what brings it into the app.

## 10. Admin features (optional in the apps)

The server's web page does all of this, so the apps don't need it. If you want any of
it anyway, show it only when `role == "admin"`:

- `POST /api/rescan` → `{ "books": N }`
- `GET /api/books/{id}/match?provider=audible|openlibrary|google…`, and
  `PUT`/`DELETE /api/books/{id}/metadata`, for matching and editing details.
- `PUT /api/books/{id}/cover` (image body) and `PUT /api/books/{id}/chapters`.
- `GET/POST /api/users` and `PATCH/DELETE /api/users/{name}`.
- Libraries, uploads, scan problems, settings, backup and activity: see the README.

## 11. Protocol document

`docs/kithara-sync-protocol.md` (in the Android repo) and the site copy still describe
version 1 as it was. Suggested edits, for whoever owns that document:

- §8 lists "Multiple libraries per server" and "Listening statistics" as left out of
  version 1. Both now exist as optional extensions. Describe them as things a server
  MAY offer, advertised in `features`.
- Add the optional book fields from §2 to §4 of that document.
- Mention `features` on `/api/health` as the way to detect extensions.
- The conformance tables (§10/§11) gain rows for:
  - `device` sent
  - `speed` sent
  - `429`/`503` on sign-in
  - `403` handled
  - the library filter
  - the devices screen
  - password change
  - stats
  - reported sessions
  - signed links
  - collections and playlists
  - single sign-on
  - transcripts

---

## To do in the apps

### Both apps, in this order

1. The §1 fixes. They're small, and they make everything after them accurate.
2. The new book fields (§2) and the library filter (§3).
3. The devices screen and password change (§4).
4. Reporting sessions (§6). Then show the server's stats as exact rather than
   estimated.
5. Collections and playlists (§7).
6. Transcripts from the server (§9), for Pro users: fetch and request them, and send
   up ones made on the phone.
7. Single sign-on (§8).
8. Downloads through the system downloader (§5), if wanted.

### Android (`Kithara-Android`)

- `net/KitharaServerApi.kt`
  - `authenticate()`: add `device` to `AuthRequest` (`Build.MODEL`, or the
    user-visible device name from `Settings.Global.DEVICE_NAME`).
  - `putProgress()`: send `speed` in `ServerProgress` when it isn't 1.0. The field
    already exists but isn't sent.
  - `failure()`: map `403` to "not allowed" instead of the generic message. `429` and
    `503` are already handled.
  - `ServerBook`: add `description`, `subtitle`, `genres`, `library`, `publisher`,
    `publishedYear`, `language`, `isbn`, `asin`. `ServerFile`: add `mimeType`. Keep
    them optional with defaults so older servers still parse.
  - New calls: `me`, `devices`, `revokeDevice`, `changePassword` (store the returned
    token), `stats`, `sessions`, `reportSessions`, `collections`, `playlists` (with
    create, update and delete), `oidcInfo`, `oidcExchange`, `downloadLink`. Read
    `features` from `health`.
- Transcripts (Pro only): for a server book, read `GET /api/books/{id}/transcript`
  into `TranscriptSegmentEntity` and `TranscriptChapterEntity` where the server has a
  chapter the phone hasn't. Offer "Transcribe on the server" when `transcription` is
  in `features`, and show its progress. After `TranscriptionService` finishes a
  chapter of a server book, `PUT` it up unless the server already has it `DONE`.
- `source/CustomSyncSource.kt`: carry the new fields into the local book, and the
  library name for the filter. After each listening session ends locally, queue it for
  `POST /api/sessions` with a stable id (a UUID saved with the session).
- UI:
  - the server settings screen gets devices, change password, and the identity
    provider button on sign-in (a Custom Tab, plus an intent filter for `kithara://oidc`)
  - the library gets a library filter and a Collections shelf
  - the book screen gets the new fields and "Add to playlist"

### iOS (`Kithara-iOS`)

- `KitharaCore/…/Protocol/KitharaServerClient.swift`: give `mapError` a distinct `403`
  case ("not allowed") instead of the generic `.http(status, message)`. Add the same
  new calls as Android.
- `KitharaCore/…/Protocol/ProtocolModels.swift`: add `subtitle`, `genres`, `library`,
  `publisher`, `publishedYear`, `language`, `isbn`, `asin` to `RemoteBook`, all
  optional (`description` is already there). Add `mimeType` to the file model.
- `Kithara/Sync/KitharaSyncSource.swift`: the `ProgressRecord` built for upload
  (around line 207) doesn't pass `speed`. Pass the book's playback rate when it isn't
  1.0. The model already has the field, and the wire-format test covers it. Queue
  finished listening sessions for `POST /api/sessions`, as on Android.
- UI: the same screens as Android. Sign-on uses `ASWebAuthenticationSession` with
  `callbackURLScheme: "kithara"`, which needs no `Info.plist` URL type.
