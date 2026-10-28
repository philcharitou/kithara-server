// kithara-server: a small, self-hosted server for the Kithara audiobook apps.
//
// It speaks the Kithara sync protocol (docs/kithara-sync-protocol.md in the app
// repository): folders of audiobooks become libraries, and each user's listening
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
	"time"
)

const serverVersion = "0.5.1"

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
	case "match":
		matchCmd(os.Args[2:])
	case "library":
		libraryCmd(os.Args[2:])
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
  kithara-server match  [--provider audible|openlibrary|google] [--apply N] BOOK_ID
                        look a book up and keep a result as its metadata
  kithara-server library list
  kithara-server library add [NAME] DIR                      (kept in the data folder)
  kithara-server library remove NAME
  kithara-server user list
  kithara-server user add NAME [--admin] [--libraries A,B]   (the first user is an admin)
  kithara-server user passwd|remove NAME
  kithara-server user role NAME admin|user
  kithara-server user libraries NAME A,B|all
  kithara-server user devices NAME
  kithara-server user signout NAME [DEVICE_ID]               (all devices without an id)
  kithara-server version

Every command takes --data DIR; serve, scan and match take --library.

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

// specs are the library folders from the flags, else the environment, else ./library
// when there is one. (Libraries added from the web page are kept in the data folder
// and come on top.)
func (d *dirs) specs() []libSpec {
	if len(d.libraries) > 0 {
		return d.libraries
	}
	def := ""
	if info, err := os.Stat("library"); err == nil && info.IsDir() {
		def = "./library"
	}
	var out []libSpec
	for _, part := range filepath.SplitList(envOr("KITHARA_LIBRARY", def)) {
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
	srv := newServer(store, lib, *name)
	srv.trustProxy = *trustProxy
	// --rescan is the default; settings.json (from the web page) overrides it, as it
	// does --name.
	srv.settings.interval = *rescan
	go srv.runRescans()
	var folders []string
	for _, sp := range specs {
		folders = append(folders, sp.Path)
	}
	log.Printf("kithara-server %s (protocol %d) serving %s on %s, %d books", serverVersion, protocolVersion, strings.Join(folders, ", "), *addr, lib.count())
	hs := &http.Server{
		Addr:    *addr,
		Handler: srv,
		// Slow or idle connections cannot pile up; no write timeout, since audio,
		// downloads and backups stream for as long as they take.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	if err := hs.ListenAndServe(); err != nil {
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
	for _, li := range lib.libraries(Account{Role: roleAdmin}) {
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
	admin := fs.Bool("admin", false, "user add: make the new user an admin")
	libraries := fs.String("libraries", "", "user add: the libraries the user may see, comma-separated (default all)")
	pos := splitArgs(fs, args[1:])
	arg := func(i int, what string) string {
		if i >= len(pos) {
			log.Fatalf("%s is required", what)
		}
		return pos[i]
	}
	store, err := openStore(d.data)
	if err != nil {
		log.Fatalf("data directory: %v", err)
	}
	switch sub {
	case "list":
		for _, a := range store.accounts() {
			libs := "all libraries"
			if a.admin() {
				libs = "everything"
			} else if len(a.Libraries) > 0 {
				libs = strings.Join(a.Libraries, ", ")
			}
			fmt.Printf("%-20s %-6s %d device(s)  %s\n", a.Name, a.Role, a.Devices, libs)
		}
	case "add", "passwd":
		name := arg(0, "a user name")
		_, existed := store.account(name)
		if sub == "add" && existed {
			log.Fatalf("user %q exists; `user passwd %s` changes the password", name, name)
		}
		if sub == "passwd" && !existed {
			log.Fatalf("no user %q", name)
		}
		pw := *password
		if pw == "" {
			pw = promptPassword()
		}
		if err := store.setPassword(name, pw); err != nil {
			log.Fatal(err)
		}
		if *admin {
			if err := store.setRole(name, roleAdmin); err != nil {
				log.Fatal(err)
			}
		}
		if *libraries != "" {
			if err := store.setLibraries(name, strings.Split(*libraries, ",")); err != nil {
				log.Fatal(err)
			}
		}
		a, _ := store.account(name)
		fmt.Printf("user %q ready (%s)\n", name, a.Role)
	case "role":
		name, role := arg(0, "a user name"), arg(1, "a role (admin or user)")
		if err := store.setRole(name, role); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s is now %s\n", name, role)
	case "libraries":
		name, list := arg(0, "a user name"), arg(1, "a comma-separated list of libraries, or all")
		var libs []string
		if !strings.EqualFold(list, "all") {
			libs = strings.Split(list, ",")
		}
		if err := store.setLibraries(name, libs); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s sees %s\n", name, list)
	case "devices":
		name := arg(0, "a user name")
		if _, ok := store.account(name); !ok {
			log.Fatalf("no user %q", name)
		}
		for _, dv := range store.devices(name, "") {
			seen := "never"
			if dv.LastSeenAt > 0 {
				seen = time.UnixMilli(dv.LastSeenAt).Format("2006-01-02 15:04")
			}
			fmt.Printf("%s  %-24s signed in %s, last seen %s\n", dv.ID, trunc(dv.Name, 24), time.UnixMilli(dv.CreatedAt).Format("2006-01-02"), seen)
		}
	case "signout":
		name := arg(0, "a user name")
		id := ""
		if len(pos) > 1 {
			id = pos[1]
		}
		n, err := store.revokeDevices(name, id)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("signed out %d device(s) of %s\n", n, name)
	case "remove":
		name := arg(0, "a user name")
		if err := store.removeUser(name); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("user %q removed\n", name)
	default:
		usage()
		os.Exit(2)
	}
}

// libraryCmd manages the libraries kept in the data folder (the ones also managed from
// the web page). A running server picks changes up on its next scan.
func libraryCmd(args []string) {
	if len(args) < 1 {
		usage()
		os.Exit(2)
	}
	sub := args[0]
	fs := flag.NewFlagSet("library "+sub, flag.ExitOnError)
	d := dirFlags(fs)
	pos := splitArgs(fs, args[1:])
	lib := newLibraries(d.specs(), d.data)
	switch sub {
	case "list":
		for _, li := range lib.libraries(Account{Role: roleAdmin}) {
			from := "data folder"
			if li.Fixed {
				from = "command line"
			}
			fmt.Printf("%-20s %s  (%s)\n", li.Name, li.Path, from)
		}
	case "add":
		var name, dir string
		switch len(pos) {
		case 1:
			dir = pos[0]
		case 2:
			name, dir = pos[0], pos[1]
		default:
			log.Fatal("usage: kithara-server library add [NAME] DIR")
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			log.Fatal(err)
		}
		if err := lib.storeLibrary(name, abs); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("added %s; a running server scans it within its --rescan interval (or rescan from the web page)\n", abs)
	case "remove":
		if len(pos) != 1 {
			log.Fatal("usage: kithara-server library remove NAME")
		}
		if err := lib.unstoreLibrary(pos[0]); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("removed %s\n", pos[0])
	default:
		usage()
		os.Exit(2)
	}
}

// splitArgs separates positional arguments from flags, so a command's flags may come
// before or after its arguments (`user add NAME --data DIR` and `user add --data DIR
// NAME` alike), and parses the flags.
func splitArgs(fs *flag.FlagSet, args []string) (positional []string) {
	var flags []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	fs.Parse(flags)
	return positional
}

// matchCmd looks a book up with a metadata provider and, with --apply N, keeps the
// N-th result for it; a running server shows it after its next rescan.
func matchCmd(args []string) {
	fs := flag.NewFlagSet("match", flag.ExitOnError)
	d := dirFlags(fs)
	provider := fs.String("provider", "audible", "audible, openlibrary or google")
	region := fs.String("region", "us", "audible region: us, uk, ca, au, de, fr, it, es, in, jp")
	title := fs.String("title", "", "search for this title instead of the book's own")
	author := fs.String("author", "", "search for this author instead of the book's own")
	asin := fs.String("asin", "", "audible: look up this product directly")
	apply := fs.Int("apply", 0, "keep result N (1 is the first) as the book's metadata and cover")
	noCover := fs.Bool("no-cover", false, "with --apply, leave the cover alone")
	clear := fs.Bool("clear", false, "forget any metadata kept for the book")
	pos := splitArgs(fs, args)
	if len(pos) != 1 {
		log.Fatal("usage: kithara-server match [flags] BOOK_ID   (ids are in the first column of `scan`)")
	}
	id := pos[0]
	lib := newLibraries(d.specs(), d.data)
	if err := lib.rescan(); err != nil {
		log.Printf("scan: %v", err)
	}
	b := lib.book(id)
	if b == nil {
		log.Fatalf("no book %q", id)
	}
	if *clear {
		clearOverride(d.data, id)
		fmt.Printf("forgot the metadata kept for %q\n", b.Title)
		return
	}
	q := MatchQuery{Provider: *provider, Region: *region, Title: *title, Author: *author, ASIN: *asin}
	if q.Title == "" && q.ASIN == "" {
		q.Title, q.Author = b.Title, firstAuthor(b.Author)
	}
	m := newMatcher()
	results, err := m.search(q)
	if err != nil {
		log.Fatal(err)
	}
	if *apply == 0 {
		fmt.Printf("%s: %q by %s\n", b.ID, b.Title, b.Author)
		for i, c := range results {
			series := ""
			if c.Series != "" {
				series = "  [" + c.Series
				if c.SeriesIndex != nil {
					series += fmt.Sprintf(" #%g", *c.SeriesIndex)
				}
				series += "]"
			}
			fmt.Printf("%2d. %s, %s (%s)%s\n", i+1, c.Title, strings.Join(c.Authors, ", "), c.PublishedYear, series)
		}
		if len(results) == 0 {
			fmt.Println("nothing found; try --title, --author or another --provider")
		} else {
			fmt.Printf("keep one with: kithara-server match --provider %s --apply N %s\n", q.Provider, id)
		}
		return
	}
	if *apply < 1 || *apply > len(results) {
		log.Fatalf("--apply %d: there are %d results", *apply, len(results))
	}
	c := results[*apply-1]
	cover := c.CoverURL
	c.CoverURL = ""
	if err := saveOverride(d.data, id, c); err != nil {
		log.Fatal(err)
	}
	if cover != "" && !*noCover {
		if err := m.saveCover(d.data, id, cover); err != nil {
			log.Printf("cover: %v", err)
		}
	}
	fmt.Printf("kept %q for %s; a running server shows it after its next rescan\n", c.Title, id)
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
