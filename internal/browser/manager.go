package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"talaria/internal/execenv"
)

// launchScript finds a Chromium and runs it, headless, speaking the DevTools
// protocol on its standard input and output. The protocol wants file
// descriptors 3 (commands in) and 4 (replies out), so those are wired to stdin
// and stdout before the browser's own are pointed at nothing. Arguments:
// profile directory, "sandbox" or "nosandbox", window size, and the browser's
// path (empty to look for one).
//
// The browser stops when its input closes, which is how Talaria stops it, and
// even if Talaria is killed the pipe closing takes the browser with it.
const launchScript = `profile=$1
case $profile in "~") profile=$HOME;; "~/"*) profile=$HOME/${profile#"~/"};; esac
bin=$4
if [ -n "$bin" ]; then
  [ -x "$bin" ] || { echo "the browser $bin was not found or cannot be run" >&2; exit 127; }
else
for c in chromium chromium-browser google-chrome google-chrome-stable chrome "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" "/Applications/Chromium.app/Contents/MacOS/Chromium"; do
  if command -v "$c" >/dev/null 2>&1; then bin=$(command -v "$c"); break; fi
  if [ -x "$c" ]; then bin=$c; break; fi
done
if [ -z "$bin" ]; then echo "no Chromium or Chrome is installed in this environment" >&2; exit 127; fi
fi
mkdir -p -- "$profile" || exit 1
flags=
[ "$2" = nosandbox ] && flags=--no-sandbox
exec "$bin" --headless=new --remote-debugging-pipe --user-data-dir="$profile" $flags \
  --no-first-run --no-default-browser-check --disable-gpu --disable-dev-shm-usage --disable-extensions \
  --disable-background-networking --disable-sync --mute-audio --lang=en-US --window-size="$3" about:blank \
  3<&0 4>&1 0</dev/null 1>/dev/null`

// Options say how a browser is run.
type Options struct {
	// ProfileDir is where the browser keeps cookies, logins and local storage, in
	// the environment's own filesystem. It is what makes a login last.
	ProfileDir string
	// NoSandbox turns off Chromium's own sandbox, which cannot run inside most
	// containers. The container is then the sandbox.
	NoSandbox bool
	// PageIdle closes a tab nobody has used for this long; BrowserIdle stops the
	// browser itself once it has no tabs for this long.
	PageIdle, BrowserIdle time.Duration
	// Binary is the browser to run, in the environment's filesystem; "" looks for
	// chromium, chromium-browser or Chrome.
	Binary string
	// AllowPrivate lets the agent open addresses on private networks and this
	// machine. Off by default: see CheckURL.
	AllowPrivate bool
}

func (o Options) pageIdle() time.Duration {
	if o.PageIdle > 0 {
		return o.PageIdle
	}
	return 10 * time.Minute
}

func (o Options) browserIdle() time.Duration {
	if o.BrowserIdle > 0 {
		return o.BrowserIdle
	}
	return 2 * time.Minute
}

// Manager owns the one browser of an environment and gives each conversation a
// tab in it. Logins are shared between them (they are one person's), but tabs
// are not. The browser starts when first needed and stops when idle.
type Manager struct {
	Env  execenv.Starter
	Opts Options

	// Resolve looks up a host's addresses; nil uses the system resolver. Tests replace it.
	Resolve func(ctx context.Context, host string) ([]string, error)

	mu       sync.Mutex
	cl       *client
	userAg   string
	pages    map[string]*Page
	started  time.Time // when the running browser began
	idleFrom time.Time // since when it has had no tabs
	closed   bool
	janitor  sync.Once
	quit     chan struct{}
}

// ErrClosed is returned once the manager has been shut down.
var ErrClosed = errors.New("the browser has been shut down")

// Page returns the tab for owner, starting the browser and opening the tab if
// need be. restarted says the tab is new: its page state was lost, which the
// caller should tell the model.
func (m *Manager) Page(ctx context.Context, owner string) (p *Page, restarted bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, false, ErrClosed
	}
	if m.pages == nil {
		m.pages = map[string]*Page{}
		m.quit = make(chan struct{})
	}
	m.janitor.Do(func() { go m.sweep() })

	// A browser that has died is forgotten, and takes its tabs with it.
	if m.cl != nil && !m.cl.alive() {
		m.dropLocked()
	}
	if m.cl == nil {
		if err := m.launchLocked(ctx); err != nil {
			return nil, false, err
		}
	}
	m.idleFrom = time.Time{}
	if page := m.pages[owner]; page != nil {
		page.touch()
		return page, false, nil
	}
	page, err := newPage(ctx, m.cl, m.userAg)
	if err != nil {
		return nil, false, err
	}
	m.pages[owner] = page
	return page, true, nil
}

// Peek returns the owner's tab if it has one, without starting anything.
func (m *Manager) Peek(owner string) *Page {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pages[owner]
}

// CloseOwner closes one conversation's tab.
func (m *Manager) CloseOwner(owner string) {
	m.mu.Lock()
	p := m.pages[owner]
	delete(m.pages, owner)
	if len(m.pages) == 0 {
		m.idleFrom = time.Now()
	}
	m.mu.Unlock()
	if p != nil {
		p.close()
	}
}

func (m *Manager) launchLocked(ctx context.Context) error {
	sandbox := "sandbox"
	if m.Opts.NoSandbox {
		sandbox = "nosandbox"
	}
	profile := m.Opts.ProfileDir
	if profile == "" {
		profile = "~/.talaria-browser"
	}
	// The browser outlives the call that started it, so it is not tied to ctx.
	proc, err := m.Env.Start(context.Background(), launchScript, profile, sandbox, "1280,800", m.Opts.Binary)
	if err != nil {
		return fmt.Errorf("cannot start the browser: %w", err)
	}
	c := newClient(proc)

	ictx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := c.call(ictx, "Browser.getVersion", nil, "")
	if err != nil {
		stderr := proc.Stderr()
		c.close()
		if stderr != "" {
			return fmt.Errorf("the browser did not start: %s", clip(stderr, 400))
		}
		return fmt.Errorf("the browser did not start: %w", err)
	}
	var v struct {
		UserAgent string `json:"userAgent"`
	}
	json.Unmarshal(raw, &v)
	// Headless Chrome says so in its user agent, and plenty of sites refuse it for that alone.
	m.userAg = strings.ReplaceAll(v.UserAgent, "HeadlessChrome", "Chrome")

	// Nothing the agent opens may save files into the workspace behind its back.
	if _, err := c.call(ictx, "Browser.setDownloadBehavior", map[string]any{"behavior": "deny"}, ""); err != nil {
		c.close()
		return err
	}
	m.cl, m.started = c, time.Now()
	return nil
}

func (m *Manager) dropLocked() {
	if m.cl != nil {
		m.cl.close()
	}
	m.cl = nil
	for _, p := range m.pages {
		p.stop()
	}
	m.pages = map[string]*Page{}
}

// sweep closes tabs and the browser when they have been idle long enough.
func (m *Manager) sweep() {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-m.quit:
			return
		case <-t.C:
			m.expire(time.Now())
		}
	}
}

var sweepEvery = 15 * time.Second

func (m *Manager) expire(now time.Time) {
	m.mu.Lock()
	var stale []*Page
	for owner, p := range m.pages {
		if p.idleAt(now) > m.Opts.pageIdle() {
			stale = append(stale, p)
			delete(m.pages, owner)
		}
	}
	var stop *client
	if m.cl != nil && len(m.pages) == 0 {
		if m.idleFrom.IsZero() {
			m.idleFrom = now
		}
		if now.Sub(m.idleFrom) > m.Opts.browserIdle() {
			stop, m.cl = m.cl, nil
		}
	}
	m.mu.Unlock()
	for _, p := range stale {
		p.close()
	}
	if stop != nil {
		stop.close()
	}
}

// Running says whether a browser process is up.
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cl != nil
}

// Close shuts the browser down for good.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	if m.quit != nil {
		close(m.quit)
	}
	m.dropLocked()
	m.mu.Unlock()
}
