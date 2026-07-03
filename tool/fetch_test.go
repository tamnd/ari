package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tamnd/ari/kernel/eval"
)

// openFetch is a fetch with the guard off, for httptest servers that
// live on loopback. The guard itself is tested separately and
// end to end.
func openFetch() fetchTool { return fetchTool{} }

func callFetch(t *testing.T, f Tool, args string) (*Result, error) {
	t.Helper()
	if err := f.ValidateInput(context.Background(), json.RawMessage(args), nil); err != nil {
		return nil, err
	}
	return f.Call(context.Background(), json.RawMessage(args), nil, nil)
}

func TestGuardBlocksTheAddressesItMust(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", "169.254.1.1", "0.0.0.0", "::1", "fe80::1",
	}
	for _, addr := range blocked {
		if err := guardAddress(net.ParseIP(addr)); err == nil {
			t.Errorf("guard must block %s", addr)
		}
	}
	allowed := []string{"93.184.216.34", "8.8.8.8", "2606:4700::1111"}
	for _, addr := range allowed {
		if err := guardAddress(net.ParseIP(addr)); err != nil {
			t.Errorf("guard must allow %s, got %v", addr, err)
		}
	}
}

// TestLoopbackURLIsBlockedEndToEnd runs the real tool against a real
// listener: the guard fires at the socket, before any bytes move.
func TestLoopbackURLIsBlockedEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "should never be reached")
	}))
	defer srv.Close()

	_, err := callFetch(t, NewFetch(), fmt.Sprintf(`{"url":%q}`, srv.URL))
	if err == nil {
		t.Fatal("a loopback URL must be refused")
	}
	if !strings.Contains(err.Error(), "fetch refused") || !strings.Contains(err.Error(), "loopback") {
		t.Errorf("err = %q, want the guard's refusal naming the class", err.Error())
	}
}

// TestRedirectHopIsReChecked: a public-looking first hop must not
// launder a redirect to the metadata endpoint, because the guard runs
// on every connection, not just the first URL (doc 04 section 9.4).
func TestRedirectHopIsReChecked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()

	// This guard admits the loopback test server and blocks only the
	// metadata address, isolating the redirect hop.
	f := fetchTool{guard: func(ip net.IP) error {
		if ip.Equal(metadataIP) {
			return errBlockedAddress("cloud metadata endpoint")
		}
		return nil
	}}
	_, err := callFetch(t, f, fmt.Sprintf(`{"url":%q}`, srv.URL))
	if err == nil {
		t.Fatal("the redirect to the metadata endpoint must be refused")
	}
	if !strings.Contains(err.Error(), "cloud metadata endpoint") {
		t.Errorf("err = %q, want the metadata refusal", err.Error())
	}
}

func TestRedirectsAreBounded(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+r.URL.Path+"x", http.StatusFound)
	}))
	defer srv.Close()

	_, err := callFetch(t, openFetch(), fmt.Sprintf(`{"url":%q}`, srv.URL))
	if err == nil {
		t.Fatal("an endless redirect chain must be refused")
	}
	if !strings.Contains(err.Error(), "stopped after 5 redirects") {
		t.Errorf("err = %q", err.Error())
	}
}

const fetchFixtureHTML = `<!doctype html>
<html>
<head><title>Sample Page</title><script>alert("stripped")</script>
<style>body { color: red }</style></head>
<body>
<nav><a href="/home">Home</a><a href="/about">About</a></nav>
<header>Site chrome</header>
<main>
<h1>The Article</h1>
<p>A paragraph with a <a href="https://example.com/doc">useful link</a> and
some <code>inline code</code>.</p>
<h2>Details</h2>
<ul><li>first point</li><li>second point</li></ul>
<pre><code>func main() {
	fmt.Println("hi")
}</code></pre>
<p>Closing text.</p>
</main>
<footer>Copyright chrome</footer>
</body>
</html>`

func TestHTMLReducesToMarkdownGolden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, fetchFixtureHTML)
	}))
	defer srv.Close()

	res, err := callFetch(t, openFetch(), fmt.Sprintf(`{"url":%q}`, srv.URL))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if strings.Contains(res.Model, "alert") || strings.Contains(res.Model, "color: red") {
		t.Error("scripts and styles must be stripped")
	}
	if strings.Contains(res.Model, "Site chrome") || strings.Contains(res.Model, "Copyright chrome") {
		t.Error("header and footer chrome must be stripped")
	}
	eval.Golden(t, "fetch_page", res.Model)
}

func TestJSONPassesThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"name":"ari","legs":6}`)
	}))
	defer srv.Close()

	res, err := callFetch(t, openFetch(), fmt.Sprintf(`{"url":%q}`, srv.URL))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if res.Model != `{"name":"ari","legs":6}` {
		t.Errorf("model = %q, want the JSON untouched", res.Model)
	}
}

func TestBinaryIsRefusedWithTypeAndSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(make([]byte, 512))
	}))
	defer srv.Close()

	_, err := callFetch(t, openFetch(), fmt.Sprintf(`{"url":%q}`, srv.URL))
	if err == nil {
		t.Fatal("a binary response must be refused")
	}
	want := fmt.Sprintf("%s is application/octet-stream (512 bytes); fetch cannot display it", srv.URL)
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestRobotsDisallowIsHonoredAndOverridable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = fmt.Fprint(w, "User-agent: *\nDisallow: /private\n")
		default:
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprint(w, "content of "+r.URL.Path)
		}
	}))
	defer srv.Close()

	_, err := callFetch(t, openFetch(), fmt.Sprintf(`{"url":"%s/private/x"}`, srv.URL))
	if err == nil {
		t.Fatal("a robots-disallowed path must be refused by default")
	}
	if !strings.Contains(err.Error(), "robots directives") || !strings.Contains(err.Error(), "ignore_robots") {
		t.Errorf("err = %q, want the robots refusal and the override hint", err.Error())
	}

	res, err := callFetch(t, openFetch(), fmt.Sprintf(`{"url":"%s/private/x","ignore_robots":true}`, srv.URL))
	if err != nil {
		t.Fatalf("the per-call override must fetch: %v", err)
	}
	if !strings.Contains(res.Model, "content of /private/x") {
		t.Errorf("model = %q", res.Model)
	}

	res, err = callFetch(t, openFetch(), fmt.Sprintf(`{"url":"%s/public/y"}`, srv.URL))
	if err != nil {
		t.Fatalf("an allowed path must fetch: %v", err)
	}
	if !strings.Contains(res.Model, "content of /public/y") {
		t.Errorf("model = %q", res.Model)
	}
}

func TestNonOKStatusIsVisible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, "nothing here")
	}))
	defer srv.Close()

	res, err := callFetch(t, openFetch(), fmt.Sprintf(`{"url":%q}`, srv.URL))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.HasPrefix(res.Model, "HTTP 404 Not Found") {
		t.Errorf("model = %q, want the status up front", res.Model)
	}
}

func TestFetchValidatesTheURL(t *testing.T) {
	cases := []struct {
		args string
		want string
	}{
		{`{"url":""}`, "url is required"},
		{`{"url":"ftp://example.com/f"}`, `url scheme must be http or https, got "ftp"`},
		{`{"url":"http://"}`, "url has no host"},
	}
	for _, c := range cases {
		_, err := callFetch(t, NewFetch(), c.args)
		if err == nil || err.Error() != c.want {
			t.Errorf("args %s: err = %v, want %q", c.args, err, c.want)
		}
	}
}

func TestFetchIsReadOnlyAndConcurrencySafe(t *testing.T) {
	f := NewFetch()
	args := json.RawMessage(`{"url":"https://example.com"}`)
	if !f.IsReadOnly(args) || !f.IsConcurrencySafe(args) {
		t.Error("fetch observes a remote resource; a research fan-out parallelizes it")
	}
	if f.MaxResultSize() != fetchMaxResult {
		t.Errorf("cap = %d, want %d", f.MaxResultSize(), fetchMaxResult)
	}
}
