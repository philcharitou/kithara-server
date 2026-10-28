// kithara-server: a small, self-hosted server for the Kithara audiobook apps.
//
// It speaks the Kithara sync protocol (docs/kithara-sync-protocol.md in the app
// repository): one folder of audiobooks becomes a library, and each user's listening
// position and bookmarks follow them between devices. No database; state is a few
// JSON files under the data directory.
//
//	kithara-server user add phil          # creates a user (prompts for a password)
//	kithara-server serve --library /srv/audiobooks --data /var/lib/kithara
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const serverVersion = "0.1.1"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		serveCmd(os.Args[2:])
	case "user":
		userCmd(os.Args[2:])
	case "scan":
		scanCmd(os.Args[2:])
	case "version":
		fmt.Println("kithara-server", serverVersion, "protocol", protocolVersion)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  kithara-server serve  [--library [NAME=]DIR ...] [--data DIR] [--addr :8080] [--name "My shelf"]
                        [--rescan 10m] [--trust-proxy]
  kithara-server scan   [--library [NAME=]DIR ...] [--data DIR]   scan once and print the catalogue
  kithara-server user add|remove|list|passwd [NAME] [--data DIR]
  kithara-server version

--library can be given more than once; each folder is a library of its own, named
after the folder unless NAME= says otherwise.

Flags can also come from the environment: KITHARA_LIBRARY (several folders separated
by `+string(os.PathListSeparator)+`), KITHARA_DATA, KITHARA_ADDR, KITHARA_NAME, KITHARA_RESCAN,
KITHARA_TRUST_PROXY=1.`)
}

// common flags shared by the subcommands
type dirs struct {
	libraries libFlag
	data      string
}

func dirFlags(fs *flag.FlagSet) *dirs {
	d := &dirs{}
	fs.Var(&d.libraries, "library", "folder holding audiobooks, as DIR or NAME=DIR (repeatable)")
	fs.StringVar(&d.data, "data", envOr("KITHARA_DATA", "./data"), "folder for users, tokens, progress and the scan cache")
	return d
}

// specs are the library folders from the flags, else the environment, else ./library.
func (d *dirs) specs() []libSpec {
	if len(d.libraries) > 0 {
		return d.libraries
	}
	var out []libSpec
	for _, part := range filepath.SplitList(envOr("KITHARA_LIBRARY", "./library")) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, parseLibSpec(part))
		}
	}
	return out
}

// libFlag collects repeated --library flags.
type libFlag []libSpec

func (f *libFlag) String() string {
	var parts []string
	for _, sp := range *f {
		parts = append(parts, sp.Path)
	}
	return strings.Join(parts, ",")
}

func (f *libFlag) Set(v string) error {
	*f = append(*f, parseLibSpec(v))
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func serveCmd(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	d := dirFlags(fs)
	addr := fs.String("addr", envOr("KITHARA_ADDR", ":8080"), "address to listen on")
	name := fs.String("name", envOr("KITHARA_NAME", "Kithara"), "library name shown in the apps")
	rescan := fs.Duration("rescan", mustDuration(envOr("KITHARA_RESCAN", "10m")), "how often to look for new or changed books (0 = only at start)")
	trustProxy := fs.Bool("trust-proxy", envOr("KITHARA_TRUST_PROXY", "") != "", "behind a reverse proxy: take client addresses from X-Forwarded-For")
	fs.Parse(args)

	store, err := openStore(d.data)
	if err != nil {
		log.Fatalf("data directory: %v", err)
	}
	if len(store.users()) == 0 {
		log.Printf("no users yet: run `kithara-server user add NAME` before connecting an app")
	}
	specs := d.specs()
	lib := newLibraries(specs, d.data)
	if err := lib.rescan(); err != nil {
		log.Printf("scan: %v", err)
	}
	if *rescan > 0 {
		go func() {
			for range time.Tick(*rescan) {
				if err := lib.rescan(); err != nil {
					log.Printf("scan: %v", err)
				}
			}
		}()
	}

	srv := newServer(store, lib, *name)
	srv.trustProxy = *trustProxy
	var folders []string
	for _, sp := range specs {
		folders = append(folders, sp.Path)
	}
	log.Printf("kithara-server %s (protocol %d) serving %s on %s, %d books", serverVersion, protocolVersion, strings.Join(folders, ", "), *addr, lib.count())
	if err := http.ListenAndServe(*addr, srv); err != nil {
		log.Fatal(err)
	}
}

func scanCmd(args []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	d := dirFlags(fs)
	fs.Parse(args)
	lib := newLibraries(d.specs(), d.data)
	if err := lib.rescan(); err != nil {
		log.Fatalf("scan: %v", err)
	}
	for _, b := range lib.snapshot().Books {
		fmt.Printf("%-12s %-14s %-40s %s  %d file(s), %d chapter(s), %s\n", b.ID, trunc(b.Library, 14), trunc(b.Title, 40), fmtDuration(b.DurationMs), len(b.Files), len(b.Chapters), b.Author)
	}
	for _, li := range lib.libraries() {
		fmt.Printf("%s: %d books, %s\n", li.Name, li.Books, fmtDuration(li.DurationMs))
	}
}

func userCmd(args []string) {
	if len(args) < 1 {
		usage()
		os.Exit(2)
	}
	sub := args[0]
	fs := flag.NewFlagSet("user "+sub, flag.ExitOnError)
	d := dirFlags(fs)
	password := fs.String("password", "", "password (prompted for when omitted)")
	rest := []string{}
	// Allow `user add NAME --data DIR` as well as `user add --data DIR NAME`.
	for _, a := range args[1:] {
		if strings.HasPrefix(a, "-") || len(rest) > 0 && strings.HasPrefix(rest[len(rest)-1], "-") {
			rest = append(rest, a)
		} else {
			rest = append([]string{a}, rest...)
		}
	}
	var name string
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		name, rest = rest[0], rest[1:]
	}
	fs.Parse(rest)
	store, err := openStore(d.data)
	if err != nil {
		log.Fatalf("data directory: %v", err)
	}
	switch sub {
	case "list":
		for _, u := range store.users() {
			fmt.Println(u)
		}
	case "add", "passwd":
		if name == "" {
			log.Fatal("a user name is required")
		}
		pw := *password
		if pw == "" {
			pw = promptPassword()
		}
		if err := store.setPassword(name, pw); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("user %q ready\n", name)
	case "remove":
		if name == "" {
			log.Fatal("a user name is required")
		}
		if err := store.removeUser(name); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("user %q removed\n", name)
	default:
		usage()
		os.Exit(2)
	}
}

func promptPassword() string {
	fmt.Fprint(os.Stderr, "password: ")
	// No terminal echo control in the standard library without x/term; read a line.
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	pw := strings.TrimRight(line, "\r\n")
	if len(pw) < 8 {
		log.Fatal("use at least 8 characters")
	}
	return pw
}

func mustDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		log.Fatalf("bad duration %q", s)
	}
	return d
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func fmtDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
}

// keep imports honest on every platform
var _ = filepath.Join
var _ = syscall.Getpid
