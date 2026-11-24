package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Plugins: small HTTP services an admin adds, which offer sources to search and books to
// bring into a library (docs/server-plugins.md). The server calls them; they never call
// the server, and they never see a user's token. Every call carries the plugin's secret,
// has a timeout and a size limit, and what comes back is checked before anything uses it.

const (
	pluginAPIVersion  = 1
	maxPlugins        = 20
	maxPluginAnswer   = 1 << 20
	pluginCallTimeout = 30 * time.Second
	pluginRecheck     = 15 * time.Minute
)

// Who may use a plugin's search or downloads.
const (
	allowEveryone = "everyone"
	allowAdmins   = "admins"
)

type Plugin struct {
	ID       string            `json:"id"`
	URL      string            `json:"url"`
	Secret   string            `json:"secret,omitempty"` // never sent to the web page
	Enabled  bool              `json:"enabled"`
	Search   string            `json:"search"`   // who may search it
	Download string            `json:"download"` // who may download from it
	SendUser bool              `json:"sendUser,omitempty"`
	Settings map[string]string `json:"settings,omitempty"` // the admin's answers to Info.Settings
	Info     *PluginInfo       `json:"info,omitempty"`     // as the plugin last described itself
	Checked  int64             `json:"checkedAt,omitempty"`
	Error    string            `json:"error,omitempty"` // why the last check failed
}

// PluginInfo is GET /kithara/plugin, checked and trimmed.
type PluginInfo struct {
	Name       string          `json:"name"`
	Version    string          `json:"version,omitempty"`
	APIVersion int             `json:"apiVersion"`
	Search     *PluginSource   `json:"search,omitempty"`
	Downloads  bool            `json:"downloads,omitempty"`
	Accepts    []string        `json:"accepts,omitempty"`
	Settings   []PluginSetting `json:"settings,omitempty"`
}

type PluginSource struct {
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	EmptyQueryTitle string `json:"emptyQueryTitle,omitempty"`
}

// PluginSetting is one field of the plugin's form in Settings.
type PluginSetting struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Type    string   `json:"type"` // text, secret, number, toggle or choice
	Choices []string `json:"choices,omitempty"`
	Default string   `json:"default,omitempty"`
	Hint    string   `json:"hint,omitempty"`
}

var (
	settingTypes = []string{"text", "secret", "number", "toggle", "choice"}
	settingIDRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,40}$`)
	linkKindRe   = regexp.MustCompile(`^[a-z0-9.+-]{1,40}$`)
)

type pluginBox struct {
	mu     sync.Mutex
	path   string
	list   []Plugin
	client *http.Client
	slots  chan struct{} // calls under way at once, to all plugins
}

func newPlugins(dir string) *pluginBox {
	p := &pluginBox{path: filepath.Join(dir, "plugins.json"), slots: make(chan struct{}, 16), client: &http.Client{
		Timeout: pluginCallTimeout,
		// The secret goes to the address the admin gave, and nowhere else.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	if err := readJSON(p.path, &p.list); err != nil {
		log.Printf("plugins.json: %v", err)
	}
	return p
}

// run checks every enabled plugin now and then, so Settings shows which are reachable.
func (p *pluginBox) run() {
	for {
		for _, pl := range p.snapshot() {
			if pl.Enabled {
				p.recheck(pl.ID)
			}
		}
		time.Sleep(pluginRecheck)
	}
}

func (p *pluginBox) snapshot() []Plugin {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.list)
}

func (p *pluginBox) get(id string) (Plugin, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := slices.IndexFunc(p.list, func(pl Plugin) bool { return pl.ID == id })
	if i < 0 {
		return Plugin{}, false
	}
	return p.list[i], true
}

// update changes one plugin and saves the list; an error from change leaves it as it was.
func (p *pluginBox) update(id string, change func(*Plugin) error) (Plugin, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := slices.IndexFunc(p.list, func(pl Plugin) bool { return pl.ID == id })
	if i < 0 {
		return Plugin{}, errNoPlugin
	}
	list := slices.Clone(p.list)
	if err := change(&list[i]); err != nil {
		return Plugin{}, err
	}
	if err := writeJSON(p.path, list); err != nil {
		return Plugin{}, err
	}
	p.list = list
	return list[i], nil
}

var errNoPlugin = errors.New("no such plugin")

// call sends one request to a plugin and decodes its JSON answer into out (when not nil).
func (p *pluginBox) call(ctx context.Context, pl Plugin, method, path string, in, out any) error {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	case <-ctx.Done():
		return errors.New("the server is busy with other plugin calls")
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(pl.URL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kithara-server/"+serverVersion)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if pl.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+pl.Secret)
	}
	res, err := p.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the address is already known; keep the reason
		}
		return fmt.Errorf("could not reach it: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxPluginAnswer+1))
	if err != nil {
		return fmt.Errorf("its answer broke off: %w", err)
	}
	if len(raw) > maxPluginAnswer {
		return errors.New("its answer is over 1 MB")
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		var e struct{ Error string }
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return fmt.Errorf("it answered %d: %s", res.StatusCode, clip(e.Error, 200))
		}
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			return fmt.Errorf("it refused the secret (%d)", res.StatusCode)
		}
		return fmt.Errorf("it answered %s", res.Status)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("its answer is not the JSON expected: %w", err)
	}
	return nil
}

// describe asks a plugin what it is, and checks the answer.
func (p *pluginBox) describe(ctx context.Context, pl Plugin) (*PluginInfo, error) {
	var in PluginInfo
	if err := p.call(ctx, pl, "GET", "/kithara/plugin", nil, &in); err != nil {
		return nil, err
	}
	return checkPluginInfo(in)
}

func checkPluginInfo(in PluginInfo) (*PluginInfo, error) {
	in.Name, in.Version = clip(strings.TrimSpace(in.Name), 80), clip(strings.TrimSpace(in.Version), 40)
	switch {
	case in.Name == "":
		return nil, errors.New("it gave no name")
	case in.APIVersion < 1:
		return nil, errors.New("it gave no apiVersion")
	case in.Search == nil && !in.Downloads:
		return nil, errors.New("it offers neither search nor downloads")
	case len(in.Accepts) > 20 || len(in.Settings) > 20:
		return nil, errors.New("it lists more than 20 kinds of link or settings")
	}
	if in.APIVersion > pluginAPIVersion {
		in.APIVersion = pluginAPIVersion // speak the older version
	}
	if s := in.Search; s != nil {
		s.Name, s.Description, s.EmptyQueryTitle = clip(strings.TrimSpace(s.Name), 80), clip(strings.TrimSpace(s.Description), 300), clip(strings.TrimSpace(s.EmptyQueryTitle), 80)
		if s.Name == "" {
			s.Name = in.Name
		}
	}
	for _, k := range in.Accepts {
		if !linkKindRe.MatchString(k) {
			return nil, fmt.Errorf("%q is not a kind of link (lower-case letters, digits, . + -)", clip(k, 40))
		}
	}
	if len(in.Accepts) > 0 && !in.Downloads {
		return nil, errors.New("it accepts links but offers no downloads")
	}
	seen := map[string]bool{}
	for i := range in.Settings {
		f := &in.Settings[i]
		f.Title, f.Default, f.Hint = clip(strings.TrimSpace(f.Title), 80), clip(f.Default, 200), clip(strings.TrimSpace(f.Hint), 200)
		switch {
		case !settingIDRe.MatchString(f.ID) || seen[f.ID]:
			return nil, fmt.Errorf("setting %q: an id is 1 to 40 letters, digits, _ . -, each once", clip(f.ID, 40))
		case !slices.Contains(settingTypes, f.Type):
			return nil, fmt.Errorf("setting %s: the type is one of %s", f.ID, strings.Join(settingTypes, ", "))
		case f.Type == "choice" && (len(f.Choices) == 0 || len(f.Choices) > 50):
			return nil, fmt.Errorf("setting %s: a choice has 1 to 50 choices", f.ID)
		}
		for j := range f.Choices {
			f.Choices[j] = clip(f.Choices[j], 80)
		}
		if f.Title == "" {
			f.Title = f.ID
		}
		seen[f.ID] = true
	}
	return &in, nil
}

// recheck asks a plugin to describe itself again and keeps what it says, or why it failed.
func (p *pluginBox) recheck(id string) (Plugin, error) {
	pl, ok := p.get(id)
	if !ok {
		return Plugin{}, errNoPlugin
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := p.describe(ctx, pl)
	return p.update(id, func(cur *Plugin) error {
		if cur.URL != pl.URL || cur.Secret != pl.Secret {
			return errors.New("changed while being checked; check it again")
		}
		cur.Checked = time.Now().UnixMilli()
		cur.Error = ""
		if err != nil {
			cur.Error = err.Error()
		} else {
			cur.Info = info
		}
		return nil
	})
}

// checkSettings keeps the answers to a plugin's own questions: known ids only, within
// the choices, trimmed. A secret left empty keeps the saved one.
func checkSettings(info *PluginInfo, saved, in map[string]string) (map[string]string, error) {
	if info == nil {
		return nil, errors.New("the plugin has not described its settings yet; check it first")
	}
	out := map[string]string{}
	for _, f := range info.Settings {
		v, given := in[f.ID]
		v = strings.TrimSpace(v)
		if !given || (f.Type == "secret" && v == "") {
			if old, ok := saved[f.ID]; ok {
				out[f.ID] = old
			}
			continue
		}
		switch {
		case len(v) > 1000:
			return nil, fmt.Errorf("%s is too long", f.Title)
		case f.Type == "choice" && v != "" && !slices.Contains(f.Choices, v):
			return nil, fmt.Errorf("%s is one of the choices given", f.Title)
		case f.Type == "toggle" && v != "" && v != "true" && v != "false":
			return nil, fmt.Errorf("%s is true or false", f.Title)
		}
		if v != "" {
			out[f.ID] = v
		}
	}
	return out, nil
}

func checkPluginURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("a plugin's address is http:// or https://, like http://my-plugin:9000")
	}
	return raw, nil
}

// forWeb leaves the secrets out: the plugin's own and those among its settings.
func (pl Plugin) forWeb() map[string]any {
	settings, saved := map[string]string{}, []string{}
	for k, v := range pl.Settings {
		if pl.Info != nil && slices.ContainsFunc(pl.Info.Settings, func(f PluginSetting) bool { return f.ID == k && f.Type == "secret" }) {
			saved = append(saved, k)
			continue
		}
		settings[k] = v
	}
	slices.Sort(saved)
	has := pl.Secret != ""
	pl.Secret, pl.Settings = "", nil
	return map[string]any{"plugin": pl, "hasSecret": has, "settings": settings, "savedSecrets": saved}
}

// ------------------------------------------------------------------ routes (admin)

func (s *server) listPlugins(w http.ResponseWriter, r *http.Request, user, token string) {
	out := []map[string]any{}
	for _, pl := range s.plugins.snapshot() {
		out = append(out, pl.forWeb())
	}
	writeJSONResponse(w, 200, map[string]any{"plugins": out, "apiVersion": pluginAPIVersion})
}

// addPlugin checks that the address answers as a plugin before keeping it.
func (s *server) addPlugin(w http.ResponseWriter, r *http.Request, user, token string) {
	var in struct{ URL, Secret string }
	if err := readBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "send {url, secret}")
		return
	}
	u, err := checkPluginURL(in.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	pl := Plugin{ID: newID(), URL: u, Secret: strings.TrimSpace(in.Secret), Enabled: true, Search: allowEveryone, Download: allowAdmins}
	info, err := s.plugins.describe(r.Context(), pl)
	if err != nil {
		writeError(w, http.StatusBadGateway, "That address doesn't answer as a Kithara plugin: "+err.Error())
		return
	}
	pl.Info, pl.Checked = info, time.Now().UnixMilli()
	p := s.plugins
	p.mu.Lock()
	switch {
	case len(p.list) >= maxPlugins:
		err = fmt.Errorf("%d plugins at most", maxPlugins)
	case slices.ContainsFunc(p.list, func(o Plugin) bool { return o.URL == u }):
		err = errors.New("that plugin is already added")
	default:
		list := append(slices.Clone(p.list), pl)
		if err = writeJSON(p.path, list); err == nil {
			p.list = list
		}
	}
	p.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSONResponse(w, http.StatusCreated, pl.forWeb())
}

// patchPlugin changes what is sent: url, secret ("" keeps it), enabled, search,
// download, sendUser, settings.
func (s *server) patchPlugin(w http.ResponseWriter, r *http.Request, user, token string) {
	var in struct {
		URL      *string           `json:"url"`
		Secret   *string           `json:"secret"`
		Enabled  *bool             `json:"enabled"`
		Search   *string           `json:"search"`
		Download *string           `json:"download"`
		SendUser *bool             `json:"sendUser"`
		Settings map[string]string `json:"settings"`
	}
	if err := readBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "send the fields to change")
		return
	}
	who := func(v *string) bool { return v == nil || *v == allowEveryone || *v == allowAdmins }
	if !who(in.Search) || !who(in.Download) {
		writeError(w, http.StatusBadRequest, "search and download are everyone or admins")
		return
	}
	moved := false
	pl, err := s.plugins.update(r.PathValue("id"), func(pl *Plugin) error {
		if in.URL != nil {
			u, err := checkPluginURL(*in.URL)
			if err != nil {
				return err
			}
			moved = moved || u != pl.URL
			pl.URL = u
		}
		if in.Secret != nil && strings.TrimSpace(*in.Secret) != "" {
			moved = moved || strings.TrimSpace(*in.Secret) != pl.Secret
			pl.Secret = strings.TrimSpace(*in.Secret)
		}
		if in.Enabled != nil {
			pl.Enabled = *in.Enabled
		}
		if in.Search != nil {
			pl.Search = *in.Search
		}
		if in.Download != nil {
			pl.Download = *in.Download
		}
		if in.SendUser != nil {
			pl.SendUser = *in.SendUser
		}
		if in.Settings != nil {
			settings, err := checkSettings(pl.Info, pl.Settings, in.Settings)
			if err != nil {
				return err
			}
			pl.Settings = settings
		}
		return nil
	})
	switch {
	case errors.Is(err, errNoPlugin):
		writeError(w, http.StatusNotFound, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if moved || (in.Enabled != nil && *in.Enabled) {
		pl, _ = s.plugins.recheck(pl.ID) // a new address or secret: see that it answers
	}
	writeJSONResponse(w, 200, pl.forWeb())
}

func (s *server) checkPlugin(w http.ResponseWriter, r *http.Request, user, token string) {
	pl, err := s.plugins.recheck(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSONResponse(w, 200, pl.forWeb())
}

func (s *server) deletePlugin(w http.ResponseWriter, r *http.Request, user, token string) {
	p := s.plugins
	p.mu.Lock()
	list := slices.DeleteFunc(slices.Clone(p.list), func(pl Plugin) bool { return pl.ID == r.PathValue("id") })
	err := writeJSON(p.path, list)
	if err == nil {
		p.list = list
	}
	p.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
