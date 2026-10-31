package main

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testPNG is a 64 by 64 picture.
func testPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{uint8(60 + x*2), uint8(90 + y), uint8(200 - x), 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// fakeNet stands in for the network. It records every URL asked for.
type fakeNet struct {
	mu       sync.Mutex
	calls    []string
	handle   func(url string) (status int, body []byte)
	block    chan struct{} // when set, requests wait for it to close
	inflight int
	peak     int
}

func (f *fakeNet) fetch(ctx context.Context, url string) (int, io.ReadCloser, error) {
	f.mu.Lock()
	f.calls = append(f.calls, url)
	f.inflight++
	f.peak = max(f.peak, f.inflight)
	block := f.block
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inflight--
		f.mu.Unlock()
	}()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		}
	}
	status, body := f.handle(url)
	return status, io.NopCloser(bytes.NewReader(body)), nil
}

func (f *fakeNet) urls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Clone(f.calls)
	slices.Sort(out)
	return out
}

func (f *fakeNet) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

const (
	apiAuthorJSON = `{"sha":"x","commit":{"message":"m"},"author":{"login":"ada","avatar_url":"https://avatars.githubusercontent.com/u/9?v=4"},"files":[]}`
	imageURL9     = "https://avatars.githubusercontent.com/u/9?s=64&v=4"
)

// githubNet answers like GitHub: the API names account 9 and every avatar
// URL gives a picture.
func githubNet() *fakeNet {
	return &fakeNet{handle: func(u string) (int, []byte) {
		switch {
		case strings.HasPrefix(u, "https://api.github.com/"):
			return 200, []byte(apiAuthorJSON)
		case strings.HasPrefix(u, "https://avatars.githubusercontent.com/"):
			return 200, testPNG()
		}
		return 404, nil
	}}
}

// fakeClock is a clock tests move by hand.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func testAvatarCache(t *testing.T, net *fakeNet, clock *fakeClock, dir string) *avatarCache {
	t.Helper()
	return newAvatarCache(avatarOptions{Dir: dir, Fetch: net.fetch, Now: clock.Now})
}

var apiReq = avatarRequest{Email: "ada@example.com", Owner: "acme", Repo: "widgets", SHA: "0123abcd"}

func TestParseGitHubRemote(t *testing.T) {
	good := map[string]githubRepo{
		"https://github.com/acme/widgets.git":                 {"acme", "widgets"},
		"https://github.com/acme/widgets":                     {"acme", "widgets"},
		"https://github.com/acme/widgets/":                    {"acme", "widgets"},
		"https://GitHub.com/Acme/Widgets.git":                 {"Acme", "Widgets"},
		"https://www.github.com/acme/widgets.git":             {"acme", "widgets"},
		"http://github.com/acme/widgets.git":                  {"acme", "widgets"},
		"https://x-access-token:s3cret@github.com/acme/w.git": {"acme", "w"},
		"https://user@github.com/acme/widgets.git":            {"acme", "widgets"},
		"git@github.com:acme/widgets.git":                     {"acme", "widgets"},
		"git@github.com:acme/widgets":                         {"acme", "widgets"},
		"github.com:acme/widgets.git":                         {"acme", "widgets"},
		"ssh://git@github.com/acme/widgets.git":               {"acme", "widgets"},
		"ssh://git@github.com:22/acme/widgets.git":            {"acme", "widgets"},
		"git+ssh://git@github.com/acme/widgets.git":           {"acme", "widgets"},
		"  https://github.com/acme/dotted.repo.name.git\n":    {"acme", "dotted.repo.name"},
		"https://github.com/acme/under_score-dash":            {"acme", "under_score-dash"},
	}
	for raw, want := range good {
		if got, ok := parseGitHubRemote(raw); !ok || got != want {
			t.Errorf("parseGitHubRemote(%q) = %+v, %v; want %+v", raw, got, ok, want)
		}
	}
	bad := []string{
		"",
		"https://gitlab.com/acme/widgets.git",
		"git@gitlab.com:acme/widgets.git",
		"https://github.example.com/acme/widgets.git",
		"https://evilgithub.com/acme/widgets.git",
		"https://github.com.evil.com/acme/widgets.git",
		"https://evil.com/github.com/acme/widgets.git",
		"https://github.com/acme",
		"https://github.com/acme/widgets/extra",
		"https://github.com/acme/widgets/tree/main",
		"https://github.com//widgets",
		"https://github.com/-acme/widgets",
		"https://github.com/acme/..",
		"https://github.com/ac me/widgets",
		"ssh://git@bitbucket.org/acme/widgets.git",
		"file:///srv/git/widgets.git",
		"/srv/git/widgets.git",
		"../widgets",
		"ftp://github.com/acme/widgets.git",
		"git@github.com",
	}
	for _, raw := range bad {
		if got, ok := parseGitHubRemote(raw); ok {
			t.Errorf("parseGitHubRemote(%q) = %+v, want no GitHub repository", raw, got)
		}
	}
}

func TestNoreplyAvatarURL(t *testing.T) {
	good := map[string]string{
		"12345+zoe@users.noreply.github.com":                "https://avatars.githubusercontent.com/u/12345?s=64",
		"12345+Zoe-X@Users.NoReply.GitHub.com":              "https://avatars.githubusercontent.com/u/12345?s=64",
		"49699333+dependabot[bot]@users.noreply.github.com": "https://avatars.githubusercontent.com/u/49699333?s=64",
		"zoe@users.noreply.github.com":                      "https://avatars.githubusercontent.com/zoe?s=64",
		"  zoe-x@users.noreply.github.com ":                 "https://avatars.githubusercontent.com/zoe-x?s=64",
	}
	for email, want := range good {
		if got, ok := noreplyAvatarURL(email); !ok || got != want {
			t.Errorf("noreplyAvatarURL(%q) = %q, %v; want %q", email, got, ok, want)
		}
	}
	for _, email := range []string{
		"", "zoe@example.com", "noreply@anthropic.com", "zoe@noreply.github.com", "zoe@users.noreply.github.com.evil.com",
		"evil@users.noreply.github.com/../x", "bot[bot]@users.noreply.github.com", "a b@users.noreply.github.com",
		"12345+@users.noreply.github.com", "+zoe@users.noreply.github.com", "../x@users.noreply.github.com",
	} {
		if got, ok := noreplyAvatarURL(email); ok {
			t.Errorf("noreplyAvatarURL(%q) = %q, want none", email, got)
		}
	}
}

func TestAvatarNoreplyWithIDSkipsTheAPI(t *testing.T) {
	net := githubNet()
	ac := testAvatarCache(t, net, &fakeClock{t: cardNow}, t.TempDir())
	req := avatarRequest{Email: "12345+zoe@users.noreply.github.com", Owner: "acme", Repo: "widgets", SHA: "0123abcd"}
	if ac.Lookup(req) != nil {
		t.Fatal("a bitmap before anything loaded")
	}
	ac.Wait()
	if ac.Lookup(req) == nil {
		t.Fatal("no bitmap after loading")
	}
	if want := []string{"https://avatars.githubusercontent.com/u/12345?s=64"}; !slices.Equal(net.urls(), want) {
		t.Fatalf("requests %v, want %v", net.urls(), want)
	}
}

func TestAvatarLegacyNoreplyUsesTheLogin(t *testing.T) {
	net := githubNet()
	ac := testAvatarCache(t, net, &fakeClock{t: cardNow}, t.TempDir())
	req := avatarRequest{Email: "zoe@users.noreply.github.com", Owner: "acme", Repo: "widgets", SHA: "0123abcd"}
	ac.Lookup(req)
	ac.Wait()
	if want := []string{"https://avatars.githubusercontent.com/zoe?s=64"}; !slices.Equal(net.urls(), want) {
		t.Fatalf("requests %v, want %v", net.urls(), want)
	}
	if ac.Lookup(req) == nil {
		t.Fatal("no bitmap")
	}
}

func TestAvatarOtherAddressesAskTheCommit(t *testing.T) {
	net := githubNet()
	var changed atomic.Int32
	clock := &fakeClock{t: cardNow}
	ac := newAvatarCache(avatarOptions{Dir: t.TempDir(), Fetch: net.fetch, Now: clock.Now, Changed: func() { changed.Add(1) }})
	if ac.Lookup(apiReq) != nil {
		t.Fatal("a bitmap before anything loaded")
	}
	ac.Wait()
	want := []string{"https://api.github.com/repos/acme/widgets/commits/0123abcd", imageURL9}
	if !slices.Equal(net.urls(), want) {
		t.Fatalf("requests %v, want %v", net.urls(), want)
	}
	if ac.Lookup(apiReq) == nil {
		t.Fatal("no bitmap after loading")
	}
	if changed.Load() != 1 {
		t.Fatalf("asked for %d redraws, want 1", changed.Load())
	}
	// Once loaded it comes from memory.
	for range 5 {
		ac.Lookup(apiReq)
	}
	ac.Wait()
	if net.count() != 2 {
		t.Fatalf("requests after more lookups: %v", net.urls())
	}
}

func TestAvatarWithoutACommitToAskGetsNothing(t *testing.T) {
	net := githubNet()
	ac := testAvatarCache(t, net, &fakeClock{t: cardNow}, t.TempDir())
	// A co-author whose address does not name an account.
	if ac.Lookup(avatarRequest{Email: "bob@example.com"}) != nil {
		t.Fatal("a bitmap for nobody")
	}
	ac.Wait()
	if net.count() != 0 {
		t.Fatalf("requests %v", net.urls())
	}
}

func TestAvatarNegativeResultsAreCached(t *testing.T) {
	for _, status := range []int{403, 404, 429, 422} {
		net := &fakeNet{handle: func(string) (int, []byte) { return status, []byte(`{"message":"no"}`) }}
		clock := &fakeClock{t: cardNow}
		dir := t.TempDir()
		ac := testAvatarCache(t, net, clock, dir)
		ac.Lookup(apiReq)
		ac.Wait()
		if net.count() != 1 {
			t.Fatalf("%d: requests %v", status, net.urls())
		}
		for range 3 {
			if ac.Lookup(apiReq) != nil {
				t.Fatalf("%d: a bitmap from a refusal", status)
			}
		}
		ac.Wait()
		if net.count() != 1 {
			t.Fatalf("%d: a refusal was asked again within the day: %v", status, net.urls())
		}

		// A new process shares the disk cache.
		clock.Add(23 * time.Hour)
		again := testAvatarCache(t, net, clock, dir)
		again.Lookup(apiReq)
		again.Wait()
		if net.count() != 1 {
			t.Fatalf("%d: a restart asked again within the day: %v", status, net.urls())
		}

		// After a day it is asked again.
		clock.Add(2 * time.Hour)
		later := testAvatarCache(t, net, clock, dir)
		later.Lookup(apiReq)
		later.Wait()
		if net.count() != 2 {
			t.Fatalf("%d: not asked again after a day: %v", status, net.urls())
		}
	}
}

func TestAvatarNegativeInMemoryExpires(t *testing.T) {
	net := &fakeNet{handle: func(string) (int, []byte) { return 403, nil }}
	clock := &fakeClock{t: cardNow}
	ac := testAvatarCache(t, net, clock, "")
	ac.Lookup(apiReq)
	ac.Wait()
	ac.Lookup(apiReq)
	ac.Wait()
	if net.count() != 1 {
		t.Fatalf("requests %v", net.urls())
	}
	clock.Add(25 * time.Hour)
	ac.Lookup(apiReq)
	ac.Wait()
	if net.count() != 2 {
		t.Fatalf("requests %v", net.urls())
	}
}

func TestAvatarFailuresOfTheNetworkAreRetriedLater(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	net := &fakeNet{handle: func(u string) (int, []byte) {
		if failing.Load() {
			return 503, nil
		}
		return githubNet().handle(u)
	}}
	clock := &fakeClock{t: cardNow}
	dir := t.TempDir()
	ac := testAvatarCache(t, net, clock, dir)
	ac.Lookup(apiReq)
	ac.Wait()
	ac.Lookup(apiReq)
	ac.Wait()
	if net.count() != 1 {
		t.Fatalf("a server error asked again at once: %v", net.urls())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a server error was written to the disk cache: %v", entries)
	}
	failing.Store(false)
	clock.Add(11 * time.Minute)
	ac.Lookup(apiReq)
	ac.Wait()
	if ac.Lookup(apiReq) == nil {
		t.Fatalf("no bitmap after the retry: %v", net.urls())
	}
}

func TestAvatarCommitWithoutAnAccount(t *testing.T) {
	for _, body := range []string{`{"sha":"x","author":null,"committer":null}`, `{"sha":"x"}`, `{"author":{"login":"a","avatar_url":""}}`} {
		net := &fakeNet{handle: func(string) (int, []byte) { return 200, []byte(body) }}
		ac := testAvatarCache(t, net, &fakeClock{t: cardNow}, t.TempDir())
		ac.Lookup(apiReq)
		ac.Wait()
		ac.Lookup(apiReq)
		ac.Wait()
		if net.count() != 1 || ac.Lookup(apiReq) != nil {
			t.Fatalf("%s: requests %v", body, net.urls())
		}
	}
}

func TestAvatarRefusesForeignHostsFromTheAPI(t *testing.T) {
	for _, avatar := range []string{"https://evil.example.com/a.png", "http://avatars.githubusercontent.com/u/1", "https://githubusercontent.com.evil.com/u/1", "/u/1", "javascript:alert(1)"} {
		net := &fakeNet{handle: func(u string) (int, []byte) {
			return 200, []byte(`{"author":{"avatar_url":"` + avatar + `"}}`)
		}}
		ac := testAvatarCache(t, net, &fakeClock{t: cardNow}, t.TempDir())
		ac.Lookup(apiReq)
		ac.Wait()
		if net.count() != 1 {
			t.Errorf("%s: followed it: %v", avatar, net.urls())
		}
	}
}

func TestAvatarReadsOnlyTheStartOfTheAPIAnswer(t *testing.T) {
	var read atomic.Int64
	net := &fakeNet{}
	big := strings.Repeat(`{"filename":"f.go","patch":"`+strings.Repeat("x", 4000)+`"},`, 5000) // 20 MB
	body := `{"sha":"x","author":{"avatar_url":"https://avatars.githubusercontent.com/u/9?v=4"},"files":[` + big + `{}]}`
	ac := newAvatarCache(avatarOptions{Now: (&fakeClock{t: cardNow}).Now, Fetch: func(ctx context.Context, u string) (int, io.ReadCloser, error) {
		net.mu.Lock()
		net.calls = append(net.calls, u)
		net.mu.Unlock()
		if strings.HasPrefix(u, "https://api.github.com/") {
			return 200, io.NopCloser(countingReader{strings.NewReader(body), &read}), nil
		}
		return 200, io.NopCloser(bytes.NewReader(testPNG())), nil
	}})
	ac.Lookup(apiReq)
	ac.Wait()
	if ac.Lookup(apiReq) == nil {
		t.Fatalf("no bitmap: %v", net.urls())
	}
	if read.Load() > 256<<10 {
		t.Fatalf("read %d bytes of the answer, which lists every file after the author", read.Load())
	}
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func TestAvatarSingleFlightAndBoundedConcurrency(t *testing.T) {
	net := githubNet()
	net.block = make(chan struct{})
	ac := testAvatarCache(t, net, &fakeClock{t: cardNow}, t.TempDir())

	// One address asked for by many frames loads once.
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); ac.Lookup(apiReq) }()
	}
	wg.Wait()

	// Many addresses load two at a time.
	for i := range 6 {
		ac.Lookup(avatarRequest{Email: "user" + string(rune('a'+i)) + "@example.com", Owner: "acme", Repo: "widgets", SHA: "0123abcd"})
	}
	time.Sleep(100 * time.Millisecond)
	net.mu.Lock()
	peak, calls := net.peak, len(net.calls)
	net.mu.Unlock()
	if peak > avatarConcurrency || calls > avatarConcurrency {
		t.Fatalf("peak %d concurrent, %d started; the bound is %d", peak, calls, avatarConcurrency)
	}
	close(net.block)
	ac.Wait()
	net.mu.Lock()
	peak = net.peak
	net.mu.Unlock()
	if peak > avatarConcurrency {
		t.Fatalf("peak %d concurrent requests, the bound is %d", peak, avatarConcurrency)
	}
	// 7 addresses, each an API request and a picture; apiReq's once.
	if got := net.count(); got != 14 {
		t.Fatalf("%d requests, want 14: %v", got, net.urls())
	}
}

func TestAvatarRequestTimesOut(t *testing.T) {
	net := githubNet()
	net.block = make(chan struct{}) // never closes
	defer close(net.block)
	ac := newAvatarCache(avatarOptions{Fetch: net.fetch, Now: (&fakeClock{t: cardNow}).Now, Timeout: 50 * time.Millisecond})
	start := time.Now()
	ac.Lookup(apiReq)
	ac.Wait()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
	if ac.Lookup(apiReq) != nil {
		t.Fatal("a bitmap after a timeout")
	}
	if net.count() != 1 {
		t.Fatalf("requests %v", net.urls())
	}
}

func TestAvatarDiskCache(t *testing.T) {
	net := githubNet()
	clock := &fakeClock{t: cardNow}
	dir := filepath.Join(t.TempDir(), "avatars", "nested")
	ac := testAvatarCache(t, net, clock, dir)
	ac.Lookup(apiReq)
	ac.Wait()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("disk cache %v, %v", entries, err)
	}
	name := entries[0].Name()
	if strings.Contains(name, "ada") || strings.Contains(name, "example") || strings.Contains(name, "@") || !strings.HasSuffix(name, ".img") {
		t.Errorf("file %q is not a hash of the address", name)
	}

	// A new process reads it without the network.
	again := testAvatarCache(t, net, clock, dir)
	again.Lookup(apiReq)
	again.Wait()
	if again.Lookup(apiReq) == nil || net.count() != 2 {
		t.Fatalf("restart did not use the disk: %v", net.urls())
	}

	// After a week it is fetched again.
	clock.Add(8 * 24 * time.Hour)
	stale := testAvatarCache(t, net, clock, dir)
	stale.Lookup(apiReq)
	stale.Wait()
	if net.count() != 4 {
		t.Fatalf("a stale picture was not fetched again: %v", net.urls())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("leftovers in the cache: %v", entries)
	}
}

func TestAvatarDiskCacheIgnoresGarbage(t *testing.T) {
	net := githubNet()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, avatarKey(apiReq.Email)+".img"), []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	ac := testAvatarCache(t, net, &fakeClock{t: time.Now()}, dir)
	ac.Lookup(apiReq)
	ac.Wait()
	if ac.Lookup(apiReq) == nil || net.count() != 2 {
		t.Fatalf("garbage in the cache stopped the fetch: %v", net.urls())
	}
}

func TestAvatarRejectsOversizedPictures(t *testing.T) {
	big := image.NewRGBA(image.Rect(0, 0, avatarMaxSide+1, 1))
	var b bytes.Buffer
	_ = png.Encode(&b, big)
	net := &fakeNet{handle: func(u string) (int, []byte) { return 200, b.Bytes() }}
	ac := testAvatarCache(t, net, &fakeClock{t: cardNow}, "")
	ac.Lookup(avatarRequest{Email: "12345+zoe@users.noreply.github.com"})
	ac.Wait()
	if ac.Lookup(avatarRequest{Email: "12345+zoe@users.noreply.github.com"}) != nil {
		t.Fatal("decoded a picture past the size limit")
	}
}

func TestAvatarSettingOffMakesNoCalls(t *testing.T) {
	net := githubNet()
	var on atomic.Bool
	ac := newAvatarCache(avatarOptions{Fetch: net.fetch, Now: (&fakeClock{t: cardNow}).Now, Enabled: on.Load})
	for range 3 {
		if ac.Lookup(apiReq) != nil {
			t.Fatal("a bitmap while off")
		}
	}
	ac.Wait()
	if net.count() != 0 {
		t.Fatalf("requests while off: %v", net.urls())
	}

	// Turned off while a load waits for its turn: it never goes out.
	net.block = make(chan struct{})
	on.Store(true)
	for i := range 4 {
		ac.Lookup(avatarRequest{Email: "user" + string(rune('a'+i)) + "@example.com", Owner: "acme", Repo: "widgets", SHA: "0123abcd"})
	}
	time.Sleep(50 * time.Millisecond)
	on.Store(false)
	close(net.block)
	ac.Wait()
	if got := net.count(); got > avatarConcurrency {
		t.Fatalf("%d requests went out, the two in flight when it turned off are the most: %v", got, net.urls())
	}
	if ac.Lookup(avatarRequest{Email: "userd@example.com", Owner: "acme", Repo: "widgets", SHA: "0123abcd"}) != nil {
		t.Fatal("a bitmap while off")
	}

	// Back on, what was held back loads.
	net.mu.Lock()
	net.block = nil
	net.mu.Unlock()
	on.Store(true)
	ac.Lookup(avatarRequest{Email: "userd@example.com", Owner: "acme", Repo: "widgets", SHA: "0123abcd"})
	ac.Wait()
	if ac.Lookup(avatarRequest{Email: "userd@example.com", Owner: "acme", Repo: "widgets", SHA: "0123abcd"}) == nil {
		t.Fatal("no bitmap after turning it back on")
	}
}

func TestAuthorAvatarURLErrors(t *testing.T) {
	for _, in := range []string{"", "[]", "not json", `{"author":`, `{"a":1,"author":{"avatar_url":3}}`} {
		if u, err := authorAvatarURL(strings.NewReader(in)); err == nil {
			t.Errorf("%q gave %q", in, u)
		}
	}
	if _, err := authorAvatarURL(strings.NewReader(`{"author":null}`)); !errors.Is(err, errNoAccount) {
		t.Errorf("author null: %v", err)
	}
	if u, err := authorAvatarURL(strings.NewReader(apiAuthorJSON)); err != nil || u != "https://avatars.githubusercontent.com/u/9?v=4" {
		t.Errorf("got %q, %v", u, err)
	}
}

func TestSizedAvatarURL(t *testing.T) {
	cases := map[string]string{
		"https://avatars.githubusercontent.com/u/9?v=4":       "https://avatars.githubusercontent.com/u/9?s=64&v=4",
		"https://avatars.githubusercontent.com/u/9":           "https://avatars.githubusercontent.com/u/9?s=64",
		"https://avatars.githubusercontent.com/u/9?s=400&v=4": "https://avatars.githubusercontent.com/u/9?s=64&v=4",
		"https://avatars1.githubusercontent.com/u/9?v=3":      "https://avatars1.githubusercontent.com/u/9?s=64&v=3",
	}
	for in, want := range cases {
		if got, ok := sizedAvatarURL(in); !ok || got != want {
			t.Errorf("sizedAvatarURL(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "http://avatars.githubusercontent.com/u/9", "https://example.com/u/9", "//avatars.githubusercontent.com/u/9"} {
		if got, ok := sizedAvatarURL(in); ok {
			t.Errorf("sizedAvatarURL(%q) = %q, want none", in, got)
		}
	}
}

// ---- The card -----------------------------------------------------------

const avatarMessage = "feat: with friends\n\nBody.\n\nCo-authored-by: Zoe <12345+zoe@users.noreply.github.com>\nCo-authored-by: Bob <bob@example.com>\n"

// avatarCardApp is an app whose card shows a commit by Ada, co-authored by
// Zoe (a noreply address) and Bob (another address).
func avatarCardApp(t *testing.T, remote string, net *fakeNet, settings *settingsStore) (*app, string, func(), *int) {
	t.Helper()
	dir, hashes := makeRepo(t, []commitSpec{{file: "a.txt", msg: avatarMessage, date: "2026-10-31T11:00:00+00:00", author: "Ada <ada@example.com>"}})
	a, tt := cardApp(t, dir, 1280, 800)
	remoteCalls := new(int)
	a.cards.remoteLoad = func(string) string { *remoteCalls++; return remote }
	a.cards.settings = settings
	a.cards.avatars = newAvatarCache(avatarOptions{
		Fetch:   net.fetch,
		Now:     (&fakeClock{t: cardNow}).Now,
		Enabled: settings.GitHubAvatars,
	})
	show := func() {
		hover(t, tt, "feat: with friends")
		a.cards.drain() // the remote resolves
		tt.Frame()
		a.cards.avatars.Wait()
		tt.Frame()
	}
	return a, hashes[0], show, remoteCalls
}

func TestCardAvatarOrder(t *testing.T) {
	net := githubNet()
	a, hash, show, _ := avatarCardApp(t, "git@github.com:acme/widgets.git", net, newSettingsStore(""))
	show()
	want := []string{
		"https://api.github.com/repos/acme/widgets/commits/" + hash, // the author, by commit
		imageURL9,
		"https://avatars.githubusercontent.com/u/12345?s=64", // Zoe, by her noreply address, without the API
	}
	slices.Sort(want)
	if !slices.Equal(net.urls(), want) {
		t.Fatalf("requests\n got %v\nwant %v", net.urls(), want)
	}
	if a.cards.avatars.Lookup(avatarRequest{Email: "12345+zoe@users.noreply.github.com"}) == nil {
		t.Fatal("Zoe has no picture")
	}
	if a.cards.avatars.Lookup(avatarRequest{Email: "ada@example.com", Owner: "acme", Repo: "widgets", SHA: hash}) == nil {
		t.Fatal("Ada has no picture")
	}
	if a.cards.avatars.Lookup(avatarRequest{Email: "bob@example.com"}) != nil {
		t.Fatal("Bob has a picture")
	}
	for _, u := range net.urls() {
		if strings.Contains(u, "bob") || strings.Contains(u, "gravatar") {
			t.Fatalf("asked about %q", u)
		}
	}
}

func TestCardAvatarIsRefusedAndRemembered(t *testing.T) {
	net := &fakeNet{handle: func(u string) (int, []byte) { return 403, []byte(`{"message":"API rate limit exceeded"}`) }}
	_, _, show, _ := avatarCardApp(t, "https://github.com/acme/widgets", net, newSettingsStore(""))
	show()
	first := net.count()
	if first == 0 {
		t.Fatal("nothing was asked")
	}
	show()
	show()
	if net.count() != first {
		t.Fatalf("a refusal was asked again: %v", net.urls())
	}
}

func TestCardNonGitHubOriginMakesNoCalls(t *testing.T) {
	for _, remote := range []string{"https://gitlab.com/acme/widgets.git", "git@bitbucket.org:acme/widgets.git", "https://github.example.com/acme/widgets.git", "/srv/git/widgets.git", ""} {
		net := githubNet()
		_, _, show, remoteCalls := avatarCardApp(t, remote, net, newSettingsStore(""))
		show()
		show()
		if net.count() != 0 {
			t.Fatalf("%q: requests %v", remote, net.urls())
		}
		if *remoteCalls != 1 {
			t.Fatalf("%q: looked up the remote %d times, want once", remote, *remoteCalls)
		}
	}
}

func TestCardSettingOffMakesNoCalls(t *testing.T) {
	net := githubNet()
	settings := newSettingsStore("")
	_ = settings.Update(func(s *Settings) { s.GitHubAvatars = false })
	a, _, show, remoteCalls := avatarCardApp(t, "git@github.com:acme/widgets.git", net, settings)
	show()
	if net.count() != 0 || *remoteCalls != 0 {
		t.Fatalf("while off: requests %v, remote lookups %d", net.urls(), *remoteCalls)
	}

	// Turned on, the avatars come.
	_ = settings.Update(func(s *Settings) { s.GitHubAvatars = true })
	show()
	if net.count() != 3 {
		t.Fatalf("after turning it on: %v", net.urls())
	}

	// Turned off again, no more calls and no pictures.
	_ = settings.Update(func(s *Settings) { s.GitHubAvatars = false })
	before := net.count()
	show()
	if net.count() != before {
		t.Fatalf("calls while off again: %v", net.urls())
	}
	if a.cardAvatar("12345+zoe@users.noreply.github.com", "", false) != nil {
		t.Fatal("a picture while off")
	}
}

func TestCardWithoutAnAvatarCacheMakesNoCalls(t *testing.T) {
	dir, _ := cardRepo(t)
	a, tt := cardApp(t, dir, 1280, 800)
	remoteCalls := 0
	a.cards.remoteLoad = func(string) string { remoteCalls++; return "git@github.com:acme/widgets.git" }
	hover(t, tt, subjectRich)
	a.cards.drain()
	tt.Frame()
	if remoteCalls != 0 {
		t.Fatalf("looked up the remote with no avatars to fetch")
	}
}
