package main

import (
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Collections and playlists: named, ordered lists of books.
//
// A collection is shared: admins make them ("Discworld in reading order", "For the
// car"), everyone sees them, and each person sees only the books in them they may see
// (a collection with none of those is not shown to them at all). A playlist is a user's
// own, seen by nobody else.

type BookList struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	BookIDs     []string `json:"bookIds"`
	UpdatedAt   int64    `json:"updatedAt"`
}

const (
	maxLists     = 200
	maxListBooks = 2000
)

var (
	errNoList    = errors.New("no such list")
	errTooMany   = errors.New("too many lists")
	errListShape = errors.New("a list needs a name (up to 100 characters) and at most 2,000 books")
)

// listStore keeps lists in one JSON file each: collections.json in the data folder,
// users/<name>/playlists.json for playlists.
type listStore struct {
	mu sync.Mutex
}

func (ls *listStore) load(path string) ([]BookList, error) {
	var lists []BookList
	err := readJSON(path, &lists)
	return lists, err
}

// change loads the lists at path, applies f and writes them back.
func (ls *listStore) change(path string, f func([]BookList) ([]BookList, error)) error {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	lists, err := ls.load(path)
	if err != nil {
		return err
	}
	if lists, err = f(lists); err != nil {
		return err
	}
	return writeJSON(path, lists)
}

func (s *server) collectionsPath() string { return filepath.Join(s.store.dir, "collections.json") }
func (s *server) playlistsPath(user string) string {
	return filepath.Join(s.store.dir, "users", user, "playlists.json")
}

// listInput is the body of a create or an edit; absent fields are left as they are.
type listInput struct {
	Name        *string  `json:"name"`
	Description *string  `json:"description"`
	BookIDs     []string `json:"bookIds"`
}

// apply checks the input and sets it on l. Books must exist and be visible to a; an
// id given twice is kept once, in its first place.
func (s *server) apply(l *BookList, in listInput, a Account) error {
	if in.Name != nil {
		l.Name = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		l.Description = clip(strings.TrimSpace(*in.Description), 2000)
	}
	if in.BookIDs != nil {
		seen := map[string]bool{}
		ids := []string{}
		for _, id := range in.BookIDs {
			if seen[id] {
				continue
			}
			if s.lib.bookFor(a, id) == nil {
				return errors.New("no such book: " + id)
			}
			seen[id] = true
			ids = append(ids, id)
		}
		l.BookIDs = ids
	}
	if l.Name == "" || len([]rune(l.Name)) > 100 || len(l.BookIDs) > maxListBooks {
		return errListShape
	}
	l.UpdatedAt = time.Now().UnixMilli()
	return nil
}

// visible is a list as one account sees it: only the books it may see (and that still
// exist), or false when that leaves nothing of a collection.
func (s *server) visible(l BookList, a Account, shared bool) (BookList, bool) {
	ids := make([]string, 0, len(l.BookIDs))
	for _, id := range l.BookIDs {
		if s.lib.bookFor(a, id) != nil {
			ids = append(ids, id)
		}
	}
	if shared && len(ids) == 0 && len(l.BookIDs) > 0 {
		return l, false
	}
	l.BookIDs = ids
	return l, true
}

// ------------------------------------------------------------------ routes

// listRoutes serves collections (shared; admins edit) or playlists (each user's own).
func (s *server) listRoutes(m *http.ServeMux, kind string, shared bool) {
	path := func(user string) string {
		if shared {
			return s.collectionsPath()
		}
		return s.playlistsPath(user)
	}
	edit := s.withUser
	if shared {
		edit = s.withAdmin
	}
	m.HandleFunc("GET /api/"+kind, s.withUser(func(w http.ResponseWriter, r *http.Request, user, token string) {
		lists, err := s.lists.load(path(user))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		a := s.acct(user)
		out := []BookList{}
		for _, l := range lists {
			if v, ok := s.visible(l, a, shared); ok {
				out = append(out, v)
			}
		}
		writeJSONResponse(w, 200, map[string]any{kind: out})
	}))
	m.HandleFunc("POST /api/"+kind, edit(func(w http.ResponseWriter, r *http.Request, user, token string) {
		var in listInput
		if err := readBody(r, &in); err != nil {
			writeError(w, http.StatusBadRequest, "bad list")
			return
		}
		l := BookList{ID: newID(), BookIDs: []string{}}
		err := s.lists.change(path(user), func(lists []BookList) ([]BookList, error) {
			if len(lists) >= maxLists {
				return nil, errTooMany
			}
			if err := s.apply(&l, in, s.acct(user)); err != nil {
				return nil, err
			}
			return append(lists, l), nil
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSONResponse(w, http.StatusCreated, l)
	}))
	m.HandleFunc("PATCH /api/"+kind+"/{id}", edit(func(w http.ResponseWriter, r *http.Request, user, token string) {
		var in listInput
		if err := readBody(r, &in); err != nil {
			writeError(w, http.StatusBadRequest, "bad list")
			return
		}
		var out BookList
		err := s.lists.change(path(user), func(lists []BookList) ([]BookList, error) {
			i := slices.IndexFunc(lists, func(l BookList) bool { return l.ID == r.PathValue("id") })
			if i < 0 {
				return nil, errNoList
			}
			if err := s.apply(&lists[i], in, s.acct(user)); err != nil {
				return nil, err
			}
			out = lists[i]
			return lists, nil
		})
		switch {
		case errors.Is(err, errNoList):
			writeError(w, http.StatusNotFound, err.Error())
		case err != nil:
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeJSONResponse(w, 200, out)
		}
	}))
	m.HandleFunc("DELETE /api/"+kind+"/{id}", edit(func(w http.ResponseWriter, r *http.Request, user, token string) {
		err := s.lists.change(path(user), func(lists []BookList) ([]BookList, error) {
			i := slices.IndexFunc(lists, func(l BookList) bool { return l.ID == r.PathValue("id") })
			if i < 0 {
				return nil, errNoList
			}
			return slices.Delete(lists, i, i+1), nil
		})
		if errors.Is(err, errNoList) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}
