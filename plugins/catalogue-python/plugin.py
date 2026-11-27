#!/usr/bin/env python3
"""A Kithara server plugin in one file of standard-library Python.

It offers the books listed in a JSON file (catalogue.json): each with a title, an author
and the addresses of its files. Search finds them by title, author or narrator, and a
download fetches the files into the folder the server made for it. Use it for files you
keep somewhere else, or as the start of a plugin of your own: see
docs/writing-a-server-plugin.md in the server's repository.

    KITHARA_PLUGIN_SECRET=...  python3 plugin.py

Settings come from the environment: KITHARA_PLUGIN_SECRET (required), KITHARA_INCOMING
(default /incoming), CATALOGUE (default catalogue.json beside this file), PORT (9000).
"""

import hmac
import json
import os
import re
import shutil
import threading
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

SECRET = os.environ.get("KITHARA_PLUGIN_SECRET", "")
INCOMING = os.environ.get("KITHARA_INCOMING", "/incoming")
CATALOGUE = os.environ.get("CATALOGUE", os.path.join(os.path.dirname(os.path.abspath(__file__)), "catalogue.json"))
PORT = int(os.environ.get("PORT", "9000"))
PER_PAGE = 24

INFO = {
    "name": "My catalogue",
    "version": "1.0.0",
    "apiVersion": 1,
    "search": {"name": "My catalogue", "description": "Books listed in catalogue.json.", "emptyQueryTitle": "Everything"},
    "downloads": True,
}

FOLDER = re.compile(r"^[A-Za-z0-9_-]{1,80}$")
downloads = {}  # download id -> what GET /kithara/downloads/{id} answers
lock = threading.Lock()


def catalogue():
    with open(CATALOGUE, encoding="utf-8") as f:
        return json.load(f)


def result(book):
    """A book as a search result: only the fields the contract names."""
    return {k: book[k] for k in ("id", "title", "author", "narrator", "cover") if book.get(k)}


def fetch(dl_id, folder, book):
    """Downloads a book's files one after another, as name.part, renamed when whole."""
    status = downloads[dl_id]
    try:
        status["state"] = "DOWNLOADING"
        files = book["files"]
        for i, url in enumerate(files):
            name = os.path.basename(urllib.parse.urlparse(url).path) or "part%d" % (i + 1)
            dest = os.path.join(folder, name)
            req = urllib.request.Request(url, headers={"User-Agent": "kithara-plugin-catalogue"})
            with urllib.request.urlopen(req, timeout=30) as src, open(dest + ".part", "wb") as out:
                shutil.copyfileobj(src, out)
            os.replace(dest + ".part", dest)
            if dl_id not in downloads:  # forgotten meanwhile
                return
            status["progress"] = (i + 1) / len(files)
        status["state"] = "FINISHED"
    except Exception as e:  # noqa: BLE001 - any failure is the download's
        status["state"], status["error"] = "FAILED", str(e)


class Plugin(BaseHTTPRequestHandler):
    def reply(self, status, body=None):
        data = json.dumps(body).encode() if body is not None else b""
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def authorised(self):
        if hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + SECRET):
            return True
        self.reply(401, {"error": "wrong secret"})
        return False

    def body(self):
        n = int(self.headers.get("Content-Length") or 0)
        return json.loads(self.rfile.read(n) or b"{}")

    def do_GET(self):
        if not self.authorised():
            return
        if self.path == "/kithara/plugin":
            return self.reply(200, INFO)
        if self.path.startswith("/kithara/downloads/"):
            with lock:
                status = downloads.get(self.path.rsplit("/", 1)[1])
            return self.reply(200, status or {"state": "FAILED", "error": "unknown download (the plugin may have restarted)"})
        self.reply(404, {"error": "not found"})

    def do_POST(self):
        if not self.authorised():
            return
        req = self.body()
        books = catalogue()
        if self.path == "/kithara/search":
            words = req.get("query", "").lower().split()
            hits = [b for b in books if all(w in " ".join([b.get("title", ""), b.get("author", ""), b.get("narrator", "")]).lower() for w in words)]
            page = max(int(req.get("page", 0)), 0)
            chunk = hits[page * PER_PAGE:(page + 1) * PER_PAGE]
            return self.reply(200, {"results": [result(b) for b in chunk], "total": len(hits), "hasMore": (page + 1) * PER_PAGE < len(hits)})
        book = next((b for b in books if b["id"] == req.get("id")), None)
        if self.path == "/kithara/preview":
            if not book:
                return self.reply(404, {"error": "no such book"})
            return self.reply(200, dict(result(book), description=book.get("description", ""), files=len(book["files"])))
        if self.path == "/kithara/download":
            folder = req.get("folder", "")
            if not book:
                return self.reply(200, {"ok": False, "error": "no such book"})
            if not FOLDER.match(folder) or not os.path.isdir(os.path.join(INCOMING, folder)):
                return self.reply(200, {"ok": False, "error": "the download's folder isn't in " + INCOMING})
            with lock:
                downloads[req["downloadId"]] = {"state": "QUEUED", "progress": 0}
            threading.Thread(target=fetch, args=(req["downloadId"], os.path.join(INCOMING, folder), book), daemon=True).start()
            return self.reply(200, {"ok": True})
        self.reply(404, {"error": "not found"})

    def do_DELETE(self):
        if not self.authorised():
            return
        if self.path.startswith("/kithara/downloads/"):
            with lock:
                downloads.pop(self.path.rsplit("/", 1)[1], None)
            return self.reply(204)
        self.reply(404, {"error": "not found"})

    def log_message(self, *args):  # quiet: the server logs what matters
        pass


if __name__ == "__main__":
    if len(SECRET) < 16:
        raise SystemExit("set KITHARA_PLUGIN_SECRET to a secret of 16 characters or more")
    os.umask(0o022)
    print("catalogue plugin on :%d, %d books, downloading into %s" % (PORT, len(catalogue()), INCOMING), flush=True)
    ThreadingHTTPServer(("", PORT), Plugin).serve_forever()
