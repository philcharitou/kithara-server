# Writing a server plugin

A plugin adds a **source** to a Kithara server: somewhere to search for audiobooks, and
optionally a way to bring them into a library. The apps and the web page show its results
in their Search tab; they never talk to the plugin, only to the server.

A plugin is a small web service, in any language, that answers six calls. This guide is
the practical part; the reasons behind it are in [server-plugins.md](server-plugins.md).

There are two working examples in [`plugins/`](../plugins):

- [`librivox`](../plugins/librivox): LibriVox's public-domain audiobooks, in Go (about
  700 lines, standard library only). Settings, paging, covers, and downloads of many files
  with progress.
- [`catalogue-python`](../plugins/catalogue-python/plugin.py): one file of standard-library
  Python that offers the books listed in a JSON file. The smallest complete plugin; start
  from it.

## The six calls

The server calls the plugin; the plugin never calls the server. Every call carries
`Authorization: Bearer <secret>`, the secret the admin gave both. Bodies are JSON.

| Call | Asked when | Answer |
|---|---|---|
| `GET /kithara/plugin` | the admin adds it, presses Check, and every 15 minutes | what it is and offers |
| `POST /kithara/search` | someone searches (or opens Search: empty `query`) | a page of results |
| `POST /kithara/preview` | someone opens a result, and before a download | one result in full |
| `POST /kithara/download` | someone adds a result to a library | `{"ok": true}`, or why not |
| `GET /kithara/downloads/{id}` | every 3 seconds while a download runs | how it is going |
| `DELETE /kithara/downloads/{id}` | when it is done, failed, or stopped | nothing (forget it) |

A plugin may offer only search (no `downloads`), or only downloads (`accepts`, below).

### Describing itself

```json
{ "name": "My catalogue", "version": "1.0.0", "apiVersion": 1,
  "search": { "name": "My catalogue", "description": "Books from the attic NAS.",
              "emptyQueryTitle": "Everything" },
  "downloads": true,
  "settings": [
    { "id": "language", "title": "Language", "type": "choice",
      "choices": ["Any", "English", "French"], "default": "Any", "hint": "Only this language." },
    { "id": "token", "title": "API token", "type": "secret" } ] }
```

- `settings` become a form under the plugin in Settings → Plugins: `text`, `secret`,
  `number`, `toggle` (`"true"`/`"false"`) or `choice`, up to 20. Every other call carries
  the admin's answers as `"settings"`, with your defaults filled in, so the plugin needs
  no storage of its own. `secret` answers are never shown again on the page.
- `emptyQueryTitle` heads what you return for an empty query, which is what people see when
  they open Search. Return something there (newest, most popular).

### Searching

```
→ { "query": "dickens", "page": 0, "settings": {…} }      (and "user", if the admin allows)
← { "results": [ { "id": "…", "title": "…", "author": "…", "narrator": "…",
                   "cover": "https://…", "year": 1843 } ],
    "total": 120, "hasMore": true }
```

- `id` is yours, up to 300 characters, and comes back in `preview` and `download`.
- `cover` is an `http(s)` address the **server** can fetch; the server fetches it, keeps
  it a month, and gives the apps its own address. A cover may be on the internet or at
  the plugin's own address (a plugin may serve its own covers), but not elsewhere on the
  server's machine or local network: those are refused.
- `total` may be left out when you don't know it. At most 100 results a page.
- Answer within 10 seconds, or the server gives up and says so.

### Previewing

```
→ { "id": "…", "settings": {…} }
← { "id", "title", "author", "narrator", "cover", "year",
    "description": "…", "lengthSeconds": 29681, "sizeBytes": 247506778, "files": 22,
    "format": "M4B with chapters", "language": "English" }
```

The server uses this for the details sheet, and for the book's name in the library: the
download lands in `Author/Title/`, with these details in a `metadata.json` beside the files
unless your download brings one of its own. HTML in the description is fine (it is turned
into plain text, paragraphs kept).

### Downloading

```
→ { "downloadId": "d_1a2b3c4d5e6f", "folder": "d_1a2b3c4d5e6f", "id": "…", "settings": {…} }
← { "ok": true }        or  { "ok": false, "error": "why" }
```

1. The server has already made `<incoming>/<folder>/`. Put the book's files there:
   audio, a cover (`cover.jpg`), and anything the scanner reads (`.cue`, `.opf`,
   `metadata.json`, `desc.txt`). Subfolders are fine two levels deep (`Disc 1/`).
2. Answer straight away and download in the background.
3. Write each file under another name (`name.part`) and rename it when it is whole.
4. Answer `GET /kithara/downloads/{id}` with
   `{ "state": "QUEUED|DOWNLOADING|PAUSED|FINISHED|FAILED", "progress": 0.42,
   "bytesPerSecond": 1200000, "sizeBytes": 314572800, "error": "…" }`.
   `FINISHED` means every file is in the folder: the server then moves them into the
   library and calls `DELETE`. `FAILED` needs an `error` people can act on.
5. On `DELETE`, stop if running and forget it. The folder is the server's to remove.
6. A download you don't know (after a restart, say): answer `200` with
   `{"state": "FAILED", "error": "…"}`. Any other answer leaves it "not answering".

### Handing off a link

A source that finds books but doesn't fetch them answers `download` with
`{"ok": true, "open": "<link>"}`. The server gives the link to a plugin whose `accepts`
lists the link's kind (its scheme, like `https`, or a file extension, like `zip`), with
`"link"` in place of `"id"`. Such a plugin describes itself with `"downloads": true,
"accepts": ["zip"]` and no `search`.

## Files and the incoming folder

The plugin and the server must see the same incoming folder: a Docker volume mounted at
`/incoming` in both, or the server's `KITHARA_INCOMING` folder if they share a machine.
The server runs as user 1000; run the plugin as user 1000 too (as the examples' images
do), or make what it writes readable and its folders writable by others.

What the server leaves out (and lists on the download as "left out"): links, hidden files,
anything that isn't audio, a picture or a file the scanner reads, and anything deeper than
two folders. A download without any audio fails. Downloads are limited to 50 GB.

## Running it beside the server

```yaml
services:
  kithara:
    image: ghcr.io/philcharitou/kithara-server:0.9
    volumes: [./books:/books, kithara-data:/data, incoming:/incoming]
  my-plugin:
    build: ./my-plugin
    restart: unless-stopped
    user: "1000"
    environment:
      KITHARA_PLUGIN_SECRET: ${MY_PLUGIN_SECRET}
    volumes: [incoming:/incoming]
volumes:
  kithara-data:
  incoming:
```

Then Settings → Plugins → add `http://my-plugin:9000` with the same secret. Nothing needs a
published port: the server reaches the plugin over Compose's own network.

## Checking it

[`plugins/plugincheck`](../plugins/plugincheck) calls a plugin the way the server does and
says what the server would refuse or mishandle:

```bash
cd plugins/plugincheck
go run . -url http://localhost:9000 -secret "$SECRET" -query dickens
go run . -url http://localhost:9000 -secret "$SECRET" -incoming /path/to/incoming -download
```

It checks the description and settings, the secret, both kinds of search and the second
page, a preview, and with `-download` a whole download: the states, the files the server
would take, and `DELETE`. `-settings '{"language":"French"}'` sends settings.

Then add it in Settings → Plugins: the page says why if it isn't answering.

## Good manners

- Answer quickly; do slow work in the background.
- Keep secrets in settings of type `secret`, not in your code.
- Be kind to the services you search: send a User-Agent that names your plugin, keep to
  their rate limits, and cache what you can.
- What a plugin offers is the responsibility of whoever wrote it and whoever runs it.
  Kithara's apps show only what a server's own plugins return, under the names they give.
