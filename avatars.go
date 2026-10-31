package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// GitHub avatars for the people on a commit card.
//
// Only repositories whose origin is on GitHub ask for them. An author whose
// address is a GitHub noreply address names their account, so the picture is
// fetched directly; for any other author the commit's page of the GitHub API
// says whose commit it is. No credential is read or sent, and an address is
// never sent anywhere: the cache keys on a hash of it, and the requests carry
// the account id or login the noreply address contains, or the commit hash.
//
// Nothing here runs in a frame: Lookup reads memory and starts a job, which
// reads the disk cache, then fetches, then redraws.

const (
	avatarPixels       = 64
	avatarPositiveTTL  = 7 * 24 * time.Hour
	avatarNegativeTTL  = 24 * time.Hour
	avatarTransientTTL = 10 * time.Minute
	avatarTimeout      = 5 * time.Second
	avatarConcurrency  = 2
	avatarMaxImage     = 1 << 20
	avatarMaxAPIBody   = 8 << 20
	avatarMaxSide      = 1024
	avatarMaxEntries   = 512
)

// avatarFetchFunc performs a GET request. The caller closes the body and
// cancels ctx.
type avatarFetchFunc func(ctx context.Context, url string) (status int, body io.ReadCloser, err error)

// avatarRequest asks for the avatar of an email address.
type avatarRequest struct {
	Email string
	// Owner, Repo and SHA name a commit of a GitHub repository the address
	// committed, which the API can tell the account of when the address is not
	// a noreply one. Empty for a co-author, who then gets a picture only
	// from a noreply address.
	Owner, Repo, SHA string
}

type avatarEntry struct {
	bmp   *ui.Bitmap // nil: no avatar
	until time.Time
}

// avatarOptions configure an avatarCache.
type avatarOptions struct {
	Dir     string          // disk cache; empty keeps none
	Fetch   avatarFetchFunc // required
	Now     func() time.Time
	Enabled func() bool   // asked before every request; nil means always
	Changed func()        // asked to redraw after an avatar loaded
	Timeout time.Duration // of each request; zero means avatarTimeout
}

// avatarCache loads, decodes and caches avatars. It is safe for use from any
// goroutine and shared by every window.
type avatarCache struct {
	opt avatarOptions
	sem chan struct{}
	wg  sync.WaitGroup

	mu       sync.Mutex
	entries  map[string]avatarEntry
	inflight map[string]bool
}

func newAvatarCache(opt avatarOptions) *avatarCache {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &avatarCache{
		opt:      opt,
		sem:      make(chan struct{}, avatarConcurrency),
		entries:  make(map[string]avatarEntry),
		inflight: make(map[string]bool),
	}
}

// sharedAvatars is the avatar cache of the process, in the user data
// directory and fetching over the network.
func sharedAvatars() *avatarCache {
	settings := sharedSettings()
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if sharedAvatarCache == nil {
		sharedAvatarCache = newAvatarCache(avatarOptions{
			Dir:     userDataPath("avatars"),
			Fetch:   httpAvatarFetch(),
			Enabled: settings.GitHubAvatars,
			Changed: func() {
				for _, w := range mygo.Windows() {
					w.Invalidate()
				}
			},
		})
	}
	return sharedAvatarCache
}

// Wait blocks until the loads in progress are done, for tests.
func (ac *avatarCache) Wait() { ac.wg.Wait() }

func avatarKey(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(sum[:16])
}

func (ac *avatarCache) enabled() bool {
	return ac.opt.Enabled == nil || ac.opt.Enabled()
}

// Lookup returns the avatar of an email address when it has loaded, and nil
// while it has not or when there is none. The first lookup of an address
// starts the load; the cache asks for a redraw when it is done. It reads only
// memory, so views call it freely.
func (ac *avatarCache) Lookup(req avatarRequest) *ui.Bitmap {
	if !ac.enabled() {
		return nil
	}
	key := avatarKey(req.Email)
	now := ac.opt.Now()
	ac.mu.Lock()
	defer ac.mu.Unlock()
	if e, ok := ac.entries[key]; ok && now.Before(e.until) {
		return e.bmp
	}
	if ac.inflight[key] {
		return nil
	}
	if _, ok := noreplyAvatarURL(req.Email); !ok && (req.Owner == "" || req.Repo == "" || req.SHA == "") {
		return nil
	}
	ac.inflight[key] = true
	ac.wg.Add(1)
	go ac.load(key, req)
	return nil
}

func (ac *avatarCache) load(key string, req avatarRequest) {
	defer ac.wg.Done()
	bmp, ttl := ac.resolve(key, req)
	ac.mu.Lock()
	delete(ac.inflight, key)
	if ttl > 0 {
		if len(ac.entries) >= avatarMaxEntries {
			ac.entries = make(map[string]avatarEntry)
		}
		ac.entries[key] = avatarEntry{bmp: bmp, until: ac.opt.Now().Add(ttl)}
	}
	ac.mu.Unlock()
	if ttl > 0 && ac.opt.Changed != nil {
		ac.opt.Changed()
	}
}

// avatarOutcome is how a fetch ended, which says how long to remember it.
type avatarOutcome uint8

const (
	outcomeImage avatarOutcome = iota // an image came back
	outcomeNone                       // there is none: a missing account, or the rate limit
	outcomeRetry                      // could not tell: the network, GitHub's servers, bad data
	outcomeOff                        // the setting turned off meanwhile
)

// resolve finds the avatar in the disk cache or on the network. A ttl of
// zero says to remember nothing, as when the setting turned off meanwhile.
func (ac *avatarCache) resolve(key string, req avatarRequest) (*ui.Bitmap, time.Duration) {
	if bmp, ttl, ok := ac.readDisk(key); ok {
		return bmp, ttl
	}

	ac.sem <- struct{}{}
	defer func() { <-ac.sem }()

	data, out := ac.fetchAvatar(req)
	switch out {
	case outcomeOff:
		return nil, 0
	case outcomeNone:
		ac.writeMiss(key)
		return nil, avatarNegativeTTL
	case outcomeRetry:
		return nil, avatarTransientTTL
	}
	bmp, ok := decodeAvatar(data)
	if !ok {
		return nil, avatarTransientTTL
	}
	ac.writeImage(key, data)
	return bmp, avatarPositiveTTL
}

// fetchAvatar gets the image: from the avatar host when the address names the
// account, otherwise from the address the commit's page of the API gives.
func (ac *avatarCache) fetchAvatar(req avatarRequest) ([]byte, avatarOutcome) {
	imageURL, ok := noreplyAvatarURL(req.Email)
	if !ok {
		apiURL := "https://api.github.com/repos/" + req.Owner + "/" + req.Repo + "/commits/" + req.SHA
		raw, out := ac.get(apiURL, avatarMaxAPIBody, func(r io.Reader) ([]byte, error) {
			u, err := authorAvatarURL(r)
			return []byte(u), err
		})
		if out != outcomeImage {
			return nil, out
		}
		if imageURL, ok = sizedAvatarURL(string(raw)); !ok {
			return nil, outcomeRetry
		}
	}
	return ac.get(imageURL, avatarMaxImage, io.ReadAll)
}

// get requests rawURL. For a 200 it returns what read made of the first limit
// bytes of the body. Any other status 500 and above, and any failure of the
// transport, is worth another try later; the rest, such as 403 for the rate
// limit and 404, are an answer.
func (ac *avatarCache) get(rawURL string, limit int64, read func(io.Reader) ([]byte, error)) ([]byte, avatarOutcome) {
	if !ac.enabled() {
		return nil, outcomeOff
	}
	timeout := ac.opt.Timeout
	if timeout <= 0 {
		timeout = avatarTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	status, body, err := ac.opt.Fetch(ctx, rawURL)
	if err != nil {
		return nil, outcomeRetry
	}
	defer body.Close()
	switch {
	case status == http.StatusOK:
	case status >= 500:
		return nil, outcomeRetry
	default:
		return nil, outcomeNone
	}
	data, err := read(io.LimitReader(body, limit))
	switch {
	case errors.Is(err, errNoAccount):
		return nil, outcomeNone
	case err != nil:
		return nil, outcomeRetry
	}
	return data, outcomeImage
}

var errNoAccount = errors.New("commit has no GitHub account")

// authorAvatarURL reads author.avatar_url from a commit of the GitHub API.
// The answer lists every changed file after the author, so this stops reading
// once it has the author.
func authorAvatarURL(r io.Reader) (string, error) {
	dec := json.NewDecoder(r)
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return "", errors.New("not an object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return "", err
		}
		if key, _ := keyTok.(string); key == "author" {
			var author *struct {
				AvatarURL string `json:"avatar_url"`
			}
			if err := dec.Decode(&author); err != nil {
				return "", err
			}
			if author == nil || author.AvatarURL == "" {
				return "", errNoAccount
			}
			return author.AvatarURL, nil
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return "", err
		}
	}
	return "", errNoAccount
}

// sizedAvatarURL checks that raw is a GitHub avatar and asks for 64 pixels.
func sizedAvatarURL(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !avatarHost(u.Hostname()) {
		return "", false
	}
	q := u.Query()
	q.Set("s", "64")
	u.RawQuery = q.Encode()
	u.User = nil
	return u.String(), true
}

func avatarHost(host string) bool {
	host = strings.ToLower(host)
	return host == "api.github.com" || host == "githubusercontent.com" || strings.HasSuffix(host, ".githubusercontent.com")
}

func decodeAvatar(data []byte) (*ui.Bitmap, bool) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > avatarMaxSide || cfg.Height > avatarMaxSide {
		return nil, false
	}
	bmp, err := ui.DecodeBitmap(data)
	if err != nil {
		return nil, false
	}
	return bmp, true
}

// The disk cache holds <key>.img, the image as fetched, and <key>.miss, an
// empty file that says there is none. Their modification times say how fresh
// they are.

func (ac *avatarCache) readDisk(key string) (*ui.Bitmap, time.Duration, bool) {
	if ac.opt.Dir == "" {
		return nil, 0, false
	}
	now := ac.opt.Now()
	img := filepath.Join(ac.opt.Dir, key+".img")
	if st, err := os.Stat(img); err == nil {
		if age := now.Sub(st.ModTime()); age < avatarPositiveTTL {
			if data, err := os.ReadFile(img); err == nil {
				if bmp, ok := decodeAvatar(data); ok {
					return bmp, avatarPositiveTTL - max(age, 0), true
				}
			}
		}
	}
	if st, err := os.Stat(filepath.Join(ac.opt.Dir, key+".miss")); err == nil {
		if age := now.Sub(st.ModTime()); age < avatarNegativeTTL {
			return nil, avatarNegativeTTL - max(age, 0), true
		}
	}
	return nil, 0, false
}

func (ac *avatarCache) writeImage(key string, data []byte) {
	if ac.opt.Dir == "" {
		return
	}
	if ac.writeStamped(key+".img", data) {
		os.Remove(filepath.Join(ac.opt.Dir, key+".miss"))
	}
}

func (ac *avatarCache) writeMiss(key string) {
	if ac.opt.Dir == "" {
		return
	}
	if ac.writeStamped(key+".miss", nil) {
		os.Remove(filepath.Join(ac.opt.Dir, key+".img"))
	}
}

// writeStamped writes a cache file, dated by the cache's clock.
func (ac *avatarCache) writeStamped(name string, data []byte) bool {
	path := filepath.Join(ac.opt.Dir, name)
	if writeFileAtomic(path, data) != nil {
		return false
	}
	now := ac.opt.Now()
	_ = os.Chtimes(path, now, now)
	return true
}

// httpAvatarFetch is the real network: GitHub's API and avatar hosts only,
// over HTTPS, with no credentials.
func httpAvatarFetch() avatarFetchFunc {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || req.URL.Scheme != "https" || !avatarHost(req.URL.Hostname()) {
				return errors.New("redirect refused")
			}
			return nil
		},
	}
	return func(ctx context.Context, rawURL string) (int, io.ReadCloser, error) {
		u, err := url.Parse(rawURL)
		if err != nil || u.Scheme != "https" || !avatarHost(u.Hostname()) {
			return 0, nil, errors.New("avatar host refused")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("User-Agent", "Arbor")
		if u.Hostname() == "api.github.com" {
			req.Header.Set("Accept", "application/vnd.github+json")
			req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		return resp.StatusCode, resp.Body, nil
	}
}

var noreplyRe = regexp.MustCompile(`(?i)^(?:(\d+)\+)?([a-z0-9](?:[a-z0-9-]{0,38})(?:\[bot\])?)@users\.noreply\.github\.com$`)

// noreplyAvatarURL is the avatar URL of an address that names a GitHub
// account: "<id>+<login>@users.noreply.github.com" and the older
// "<login>@users.noreply.github.com". With an id it is the account's picture
// directly; the older form has only the login.
func noreplyAvatarURL(email string) (string, bool) {
	m := noreplyRe.FindStringSubmatch(strings.TrimSpace(email))
	if m == nil {
		return "", false
	}
	if m[1] != "" {
		return "https://avatars.githubusercontent.com/u/" + m[1] + "?s=64", true
	}
	if strings.Contains(m[2], "[") {
		return "", false
	}
	return "https://avatars.githubusercontent.com/" + m[2] + "?s=64", true
}

// githubRepo names a repository on github.com.
type githubRepo struct {
	Owner, Name string
}

var (
	ownerRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	repoRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// parseGitHubRemote parses the URL of a remote on github.com, in its https
// and ssh forms. Credentials in the URL are ignored.
func parseGitHubRemote(raw string) (githubRepo, bool) {
	raw = strings.TrimSpace(raw)
	var host, path string
	if !strings.Contains(raw, "://") {
		// scp-like: [user@]github.com:owner/repo.git
		hostPart, rest, ok := strings.Cut(raw, ":")
		if !ok {
			return githubRepo{}, false
		}
		if i := strings.LastIndex(hostPart, "@"); i >= 0 {
			hostPart = hostPart[i+1:]
		}
		host, path = hostPart, rest
	} else {
		u, err := url.Parse(raw)
		if err != nil {
			return githubRepo{}, false
		}
		switch strings.ToLower(u.Scheme) {
		case "https", "http", "ssh", "git+ssh", "git":
		default:
			return githubRepo{}, false
		}
		host, path = u.Hostname(), u.Path
	}
	if h := strings.ToLower(host); h != "github.com" && h != "www.github.com" {
		return githubRepo{}, false
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	owner, name, ok := strings.Cut(path, "/")
	if !ok || strings.Contains(name, "/") || !ownerRe.MatchString(owner) || !repoRe.MatchString(name) || name == "." || name == ".." {
		return githubRepo{}, false
	}
	return githubRepo{Owner: owner, Name: name}, true
}
