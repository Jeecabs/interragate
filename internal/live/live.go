// Package live serves the latest report to a browser on this machine while
// interragate watch re-plans, and tells the page as soon as there is a newer
// one or the plan fails.
//
// The server listens on the loopback interface only, and every URL carries
// a random token, so neither another user on the machine nor a web page
// using DNS rebinding can read the report.
package live

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// State is what the page needs to know about the watch loop.
type State struct {
	// Version numbers the reports, from 1. A page showing an older one
	// reloads.
	Version int `json:"version"`
	// Planning is true while a plan runs.
	Planning bool `json:"planning"`
	// Changed lists the files whose change started the current plan.
	Changed []string `json:"changed,omitempty"`
	// Error is why the last plan failed, if it did.
	Error string `json:"error,omitempty"`
	// At is when the state last changed, RFC 3339.
	At string `json:"at"`
	// History lists the reports of the session that can still be opened,
	// oldest first; the last is the current one.
	History []Entry `json:"history,omitempty"`
}

// Entry is one report the session published.
type Entry struct {
	Version int `json:"version"`
	// At is when it was published, RFC 3339.
	At string `json:"at"`
	// Risk is the report's risk level, such as "high".
	Risk     string `json:"risk"`
	Headline string `json:"headline,omitempty"`
	// Changed lists the files whose change led to it.
	Changed []string `json:"changed,omitempty"`
}

// keep is how many reports a session keeps, the current one included.
const keep = 12

// Server is the live report server.
type Server struct {
	mu    sync.Mutex
	state State
	page  []byte
	past  map[int][]byte // earlier reports by version, as served once newer ones exist, gzipped
	subs  map[chan struct{}]bool
	token string
	ln    net.Listener
	srv   *http.Server
	title string
}

// Start listens on addr, which must be a loopback address such as
// "127.0.0.1:0" (0 picks a free port), and serves in the background.
// title names the directory being watched, for the page shown before the
// first report.
func Start(addr, title string) (*Server, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("live: %s is not a loopback address", addr)
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &Server{
		token: hex.EncodeToString(b),
		subs:  map[chan struct{}]bool{},
		past:  map[int][]byte{},
		ln:    ln,
		title: title,
		state: State{Planning: true, At: now()},
	}
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: idleTimeout}
	go s.srv.Serve(ln)
	return s, nil
}

// idleTimeout closes a kept-alive connection that has gone this long
// without a request. An event stream is a request still being answered,
// with a ping every 20 seconds, so it stays open.
var idleTimeout = time.Minute

// URL is the page's address.
func (s *Server) URL() string {
	return fmt.Sprintf("http://%s/%s/", s.ln.Addr().String(), s.token)
}

// Close stops serving. Open event streams end.
func (s *Server) Close() error { return s.srv.Close() }

// Planning says a plan has started because files changed.
func (s *Server) Planning(changed []string) {
	s.update(func(st *State) {
		st.Planning = true
		st.Changed = changed
	})
}

// Publish serves a new report page. It must embed the state's next
// version, which Next returns, so the page knows it is current. archived
// is the same report as it is to be served once a newer one exists, at
// v/VERSION/; nil keeps no copy. e describes it in the session's history.
func (s *Server) Publish(page, archived []byte, e Entry) {
	var packed []byte
	if archived != nil {
		packed = gzipped(archived)
	}
	s.mu.Lock()
	s.page = page
	s.mu.Unlock()
	s.update(func(st *State) {
		st.Version++
		st.Planning = false
		st.Changed = nil
		st.Error = ""
		e.Version, e.At = st.Version, now()
		if packed != nil {
			s.past[st.Version] = packed
		}
		st.History = append(st.History, e)
		if n := len(st.History); n > keep {
			for _, old := range st.History[:n-keep] {
				delete(s.past, old.Version)
			}
			st.History = append([]Entry(nil), st.History[n-keep:]...)
		}
	})
}

func gzipped(b []byte) []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

// Next is the version the next published page will have.
func (s *Server) Next() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Version + 1
}

// Failed says the plan failed; pages keep showing the last report.
func (s *Server) Failed(msg string) {
	s.update(func(st *State) {
		st.Planning = false
		st.Error = msg
	})
}

func (s *Server) update(f func(*State)) {
	s.mu.Lock()
	f(&s.state)
	s.state.At = now()
	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default: // a wake-up is already pending; it will read the latest state
		}
	}
	s.mu.Unlock()
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.allowedHost(r.Host) {
		http.Error(w, "interragate watch only answers on the loopback address it printed", http.StatusForbidden)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/"+s.token+"/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if v, ok := strings.CutPrefix(rest, "v/"); ok {
		s.serveEarlier(w, r, strings.TrimSuffix(v, "/"))
		return
	}
	switch rest {
	case "":
		s.servePage(w)
	case "events":
		s.serveEvents(w, r)
	case "state":
		s.mu.Lock()
		st := s.state
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(st)
	default:
		http.NotFound(w, r)
	}
}

// allowedHost accepts only the loopback names a browser would send for the
// printed URL, which is what defeats DNS rebinding.
func (s *Server) allowedHost(host string) bool {
	_, port, _ := net.SplitHostPort(s.ln.Addr().String())
	for _, name := range []string{"127.0.0.1", "localhost", "[::1]"} {
		if host == name+":"+port {
			return true
		}
	}
	return false
}

// serveEarlier serves a report the session kept. The current one lives at
// the page's own address, so a link to its version goes there.
func (s *Server) serveEarlier(w http.ResponseWriter, r *http.Request, v string) {
	n, err := strconv.Atoi(v)
	s.mu.Lock()
	packed, current := s.past[n], s.state.Version
	s.mu.Unlock()
	switch {
	case err != nil:
		http.NotFound(w, r)
		return
	case n == current:
		http.Redirect(w, r, "/"+s.token+"/", http.StatusSeeOther)
		return
	case packed == nil:
		http.Error(w, "interragate watch keeps the last "+strconv.Itoa(keep)+" reports, and this is not one of them any more.", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", pagePolicy)
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(packed)
		return
	}
	zr, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	io.Copy(w, zr)
}

// pagePolicy lets a report run its own script and reach the event stream,
// and nothing else.
const pagePolicy = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; font-src data:; img-src data: blob:; connect-src 'self'"

func (s *Server) servePage(w http.ResponseWriter) {
	s.mu.Lock()
	page := s.page
	st := s.state
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// The report makes no requests of its own beyond the event stream.
	w.Header().Set("Content-Security-Policy", pagePolicy)
	if page != nil {
		w.Write(page)
		return
	}
	waiting.Execute(w, map[string]any{"Title": s.title, "Error": st.Error})
}

func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.subs[ch] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	send := func() bool {
		s.mu.Lock()
		b, _ := json.Marshal(s.state)
		s.mu.Unlock()
		_, err := fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
		return err == nil
	}
	if !send() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			if !send() {
				return
			}
		case <-ping.C:
			// A comment keeps proxies and the browser from timing out.
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// waiting is the page before the first report exists: it says what is
// happening and reloads when the report is ready.
var waiting = template.Must(template.New("waiting").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<title>{{.Title}} · interragate</title>
<style>
  :root { color-scheme: light dark; --page: #f7f6f3; --ink: #121211; --muted: #73716b; --bad: #bf3f76; }
  @media (prefers-color-scheme: dark) { :root { --page: #0f0f0e; --ink: #ecebe7; --muted: #9a988f; --bad: #f08cb6; } }
  html { background: var(--page); }
  body { margin: 0; min-height: 100dvh; display: grid; place-items: center; color: var(--ink); font: 15px/1.6 ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif; }
  main { max-width: 640px; padding: 40px; }
  h1 { font-size: 28px; line-height: 1.2; margin: 0 0 12px; font-weight: 600; letter-spacing: -0.02em; }
  p { margin: 0; color: var(--muted); }
  pre { margin: 20px 0 0; white-space: pre-wrap; font: 13px/1.55 ui-monospace, "SF Mono", Menlo, Consolas, monospace; color: var(--ink); }
  .bad { color: var(--bad); }
</style>
</head>
<body>
<main>
{{if .Error}}
  <h1>The plan for {{.Title}} failed.</h1>
  <p>interragate shows the report as soon as a plan succeeds. Fix the problem and save; it plans again.</p>
  <pre class="bad">{{.Error}}</pre>
{{else}}
  <h1>Planning {{.Title}}…</h1>
  <p>The report appears here when the plan finishes, and updates whenever you save a change.</p>
{{end}}
</main>
<script>
  // Reload for the first report, or for a failure other than the one shown.
  var shown = {{.Error}};
  new EventSource("events").onmessage = function (e) {
    var s = JSON.parse(e.data);
    if (s.version > 0 || (!s.planning && (s.error || "") !== shown)) location.reload();
  };
</script>
</body>
</html>
`))
