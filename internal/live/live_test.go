package live

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func get(t *testing.T, url, host string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestServer(t *testing.T) {
	s, err := Start("127.0.0.1:0", "infra")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	url := s.URL()

	// Before the first report: a page that says a plan is running.
	if code, body := get(t, url, ""); code != 200 || !strings.Contains(body, "Planning infra") {
		t.Errorf("waiting page: %d %q", code, body)
	}
	// Without the token, nothing.
	root := strings.TrimSuffix(url, s.token+"/")
	if code, _ := get(t, root, ""); code != 404 {
		t.Errorf("no token: %d, want 404", code)
	}
	// A rebound name for the same address is refused.
	if code, _ := get(t, url, "evil.example:80"); code != 403 {
		t.Errorf("foreign Host: %d, want 403", code)
	}

	if s.Next() != 1 {
		t.Errorf("first version = %d", s.Next())
	}
	s.Publish([]byte("<p>report 1</p>"), nil, Entry{})
	if code, body := get(t, url, ""); code != 200 || body != "<p>report 1</p>" {
		t.Errorf("published page: %d %q", code, body)
	}
}

func TestFailedBeforeFirstReport(t *testing.T) {
	s, err := Start("127.0.0.1:0", "infra")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Failed("Error: Unsupported argument <script>")
	_, body := get(t, s.URL(), "")
	if !strings.Contains(body, "The plan for infra failed.") || !strings.Contains(body, "Error: Unsupported argument &lt;script&gt;") {
		t.Errorf("error page:\n%s", body)
	}
}

func TestEvents(t *testing.T) {
	s, err := Start("127.0.0.1:0", "infra")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, err := http.Get(s.URL() + "events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	lines := bufio.NewScanner(res.Body)
	next := func() State {
		t.Helper()
		done := make(chan State, 1)
		go func() {
			for lines.Scan() {
				if data, ok := strings.CutPrefix(lines.Text(), "data: "); ok {
					var st State
					json.Unmarshal([]byte(data), &st)
					done <- st
					return
				}
			}
		}()
		select {
		case st := <-done:
			return st
		case <-time.After(5 * time.Second):
			t.Fatal("no event")
		}
		return State{}
	}

	// The current state arrives on connect, then every change.
	if st := next(); !st.Planning || st.Version != 0 {
		t.Errorf("initial state %+v", st)
	}
	s.Publish([]byte("page"), nil, Entry{})
	if st := next(); st.Planning || st.Version != 1 {
		t.Errorf("after publish %+v", st)
	}
	s.Planning([]string{"main.tf"})
	if st := next(); !st.Planning || len(st.Changed) != 1 {
		t.Errorf("planning %+v", st)
	}
	s.Failed("boom")
	if st := next(); st.Planning || st.Error != "boom" || st.Version != 1 {
		t.Errorf("failed %+v", st)
	}
}

// Connections kept alive with nothing to ask are closed after a while; an
// event stream, still being answered, is not.
func TestIdleConnectionsClose(t *testing.T) {
	old := idleTimeout
	idleTimeout = 200 * time.Millisecond
	t.Cleanup(func() { idleTimeout = old })
	s, err := Start("127.0.0.1:0", "infra")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	conn, err := net.Dial("tcp", s.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /%s/state HTTP/1.1\r\nHost: %s\r\n\r\n", s.token, s.ln.Addr())
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("an idle kept-alive connection: read gave %v, want the server to close it", err)
	}

	stream, err := http.Get(s.URL() + "events")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	lines := bufio.NewScanner(stream.Body)
	lines.Scan() // the state on connect
	time.Sleep(3 * idleTimeout)
	s.Publish([]byte("page"), nil, Entry{})
	got := make(chan string, 1)
	go func() {
		for lines.Scan() {
			if data, ok := strings.CutPrefix(lines.Text(), "data: "); ok {
				got <- data
				return
			}
		}
		got <- ""
	}()
	select {
	case data := <-got:
		if !strings.Contains(data, `"version":1`) {
			t.Errorf("after the idle timeout the stream gave %q", data)
		}
	case <-time.After(5 * time.Second):
		t.Error("the event stream went quiet")
	}
}

func TestLoopbackOnly(t *testing.T) {
	if _, err := Start("0.0.0.0:0", "x"); err == nil {
		t.Error("listening on every interface should be refused")
	}
}

// The session keeps its last reports, each at an address of its own, and
// lists them in the state.
func TestHistory(t *testing.T) {
	s, err := Start("127.0.0.1:0", "infra")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for v := 1; v <= keep+2; v++ {
		page := fmt.Sprintf("<p>report %d</p>", v)
		s.Publish([]byte(page), []byte("<p>earlier report "+fmt.Sprint(v)+"</p>"), Entry{Risk: "low", Changed: []string{"main.tf"}})
	}
	_, body := get(t, s.URL()+"state", "")
	var st State
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.History) != keep || st.History[0].Version != 3 || st.History[keep-1].Version != keep+2 || st.History[0].Risk != "low" || st.History[0].At == "" {
		t.Fatalf("history = %+v", st.History)
	}
	if code, body := get(t, s.URL()+"v/5/", ""); code != 200 || body != "<p>earlier report 5</p>" {
		t.Errorf("v/5: %d %q", code, body)
	}
	if code, _ := get(t, s.URL()+"v/2/", ""); code != 404 {
		t.Errorf("a report no longer kept: %d, want 404", code)
	}
	// The current report's version leads to the page itself.
	if code, body := get(t, s.URL()+fmt.Sprintf("v/%d/", keep+2), ""); code != 200 || body != fmt.Sprintf("<p>report %d</p>", keep+2) {
		t.Errorf("current version: %d %q", code, body)
	}
	if code, _ := get(t, s.URL()+"v/x/", ""); code != 404 {
		t.Errorf("v/x: %d, want 404", code)
	}
}
