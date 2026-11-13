// Package routing is what Talaria learns about which model suits which kind of message. It records how replies were
// received (a thumbs up or down, a retry on a stronger model, a correction, or simply carrying on) against the kind of
// message and the profile that answered, and, once there is enough of it, tells the router which profile has done best.
// With little or no data it says nothing, so the router's own rules stand.
package routing

import (
	"encoding/json"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Kind is what a message is asking for, in the few classes routing distinguishes.
type Kind string

const (
	Code    Kind = "code"    // programming
	Hard    Kind = "hard"    // proofs, design, deep reasoning
	Simple  Kind = "simple"  // short and plain
	Writing Kind = "writing" // drafting and rewording
	Long    Kind = "long"    // a very long conversation or document
	Vision  Kind = "vision"  // a message about a picture, which needs a model that can see
	General Kind = "general" // anything else
)

// Kinds lists them in the order they are shown.
var Kinds = []Kind{Vision, Code, Hard, Simple, Writing, Long, General}

// Valid says whether k is one of them.
func Valid(k Kind) bool { return slices.Contains(Kinds, k) }

// Parse finds the first kind named in a classifier's answer, so a model that adds words around it still works.
func Parse(answer string) (Kind, bool) {
	a := strings.ToLower(answer)
	best, at := Kind(""), len(a)+1
	for _, k := range Kinds {
		if i := strings.Index(a, string(k)); i >= 0 && i < at {
			best, at = k, i
		}
	}
	return best, best != ""
}

var (
	codeSignal = regexp.MustCompile("(?i)```|\\b(func|function|class|struct|import|error|traceback|debug|refactor|compile|regex|recursion|pointer)\\b")
	hardSignal = regexp.MustCompile("(?i)\\b(prove|proof|theorem|derive|derivation|complexity|architect|trade-?off|research deeply)\\b")
	// a reply taken badly: said at the start of the next message
	correction = regexp.MustCompile(`(?i)^\s*(no[,.!\s]|nope|wrong|that'?s (wrong|not (right|correct|what))|not what i (asked|meant)|incorrect|try again|you (misunderstood|got it wrong))`)
)

// KindOf is the rule of thumb, used when nothing better says what a message is: code and hard-thinking words, then
// length.
func KindOf(text string) Kind {
	switch {
	case codeSignal.MatchString(text):
		return Code
	case hardSignal.MatchString(text):
		return Hard
	case len([]rune(text)) < 80:
		return Simple
	}
	return General
}

var pictureRef = regexp.MustCompile(`(?i)(uploaded [^\n]*\.(png|jpe?g|gif|webp|heic|bmp)\b|/api/files/[0-9a-f]+[^\s]*\.(png|jpe?g|gif|webp)\b|\b(this|the|attached|that) (photo|picture|image|screenshot)\b)`)

// MentionsPicture says whether a piece of text is about a picture: an image file that was sent, or "this screenshot".
func MentionsPicture(text string) bool { return pictureRef.MatchString(text) }

// IsCorrection says whether a message opens by saying the last reply was wrong.
func IsCorrection(text string) bool { return correction.MatchString(text) }

// Signals a reply can get.
const (
	Up         = "up"         // a thumbs up
	Down       = "down"       // a thumbs down
	Retry      = "retry"      // the person asked for it again on a stronger model
	Correction = "correction" // the next message said it was wrong
	Continue   = "continue"   // the person simply carried on: a weak good sign
)

// weights: how much each signal says about the reply, and how much it counts as evidence.
var weight = map[string]float64{Up: 1, Down: -1, Retry: -1.5, Correction: -1, Continue: 0.15}
var evidence = map[string]float64{Up: 1, Down: 1, Retry: 1, Correction: 1, Continue: 0.4}

// Count is what has been recorded for one profile on one kind.
type Count struct {
	Up, Down, Retry, Correction, Continue int
}

func (c *Count) add(signal string) {
	switch signal {
	case Up:
		c.Up++
	case Down:
		c.Down++
	case Retry:
		c.Retry++
	case Correction:
		c.Correction++
	case Continue:
		c.Continue++
	}
}

// Score is how well the profile has done, from -1.5 to 1, and how much evidence stands behind it. It starts from a
// mild prior, so a profile with no history neither wins nor loses.
func (c Count) Score() (score, evidenceN float64) {
	const priorScore, priorN = 0.15, 4.0
	w := float64(c.Up)*weight[Up] + float64(c.Down)*weight[Down] + float64(c.Retry)*weight[Retry] +
		float64(c.Correction)*weight[Correction] + float64(c.Continue)*weight[Continue]
	n := float64(c.Up)*evidence[Up] + float64(c.Down)*evidence[Down] + float64(c.Retry)*evidence[Retry] +
		float64(c.Correction)*evidence[Correction] + float64(c.Continue)*evidence[Continue]
	return (w + priorScore*priorN) / (n + priorN), n
}

// Store keeps the counts in a small file. It is safe to use from many goroutines.
type Store struct {
	Path string
	// Enough is how much evidence a profile needs on a kind before its record can change a choice. Zero means 5.
	Enough float64
	// Margin is how much better than the usual choice a profile must be. Zero means 0.2.
	Margin float64

	mu     sync.Mutex
	loaded bool
	data   map[string]map[string]*Count // kind -> profile -> counts
}

func (s *Store) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.data = map[string]map[string]*Count{}
	if s.Path == "" {
		return
	}
	if raw, err := os.ReadFile(s.Path); err == nil {
		json.Unmarshal(raw, &s.data)
	}
}

func (s *Store) save() {
	if s.Path == "" {
		return
	}
	raw, err := json.MarshalIndent(s.data, "", " ")
	if err != nil {
		return
	}
	tmp := s.Path + ".tmp"
	if os.MkdirAll(filepath.Dir(s.Path), 0o700) == nil && os.WriteFile(tmp, raw, 0o600) == nil {
		os.Rename(tmp, s.Path)
	}
}

// Record notes how a reply from profile, to a message of the given kind, was received.
func (s *Store) Record(kind Kind, profile, signal string) {
	if s == nil || profile == "" || !Valid(kind) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	k := s.data[string(kind)]
	if k == nil {
		k = map[string]*Count{}
		s.data[string(kind)] = k
	}
	c := k[profile]
	if c == nil {
		c = &Count{}
		k[profile] = c
	}
	c.add(signal)
	s.save()
}

// Prefer picks, among candidates, the profile that has done best on this kind, when the record is strong enough to
// say. usual is what the router would pick without it: it is only passed over by a profile with enough evidence that
// is clearly better. It returns usual when there is nothing to go on, and says why when it changes the choice.
func (s *Store) Prefer(kind Kind, candidates []string, usual string) (pick, why string) {
	if s == nil || len(candidates) < 2 {
		return usual, ""
	}
	enough, margin := s.Enough, s.Margin
	if enough <= 0 {
		enough = 5
	}
	if margin <= 0 {
		margin = 0.2
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	counts := s.data[string(kind)]
	score := func(name string) (float64, float64) {
		if c := counts[name]; c != nil {
			return c.Score()
		}
		return Count{}.Score()
	}
	base, _ := score(usual)
	pick, best := usual, base
	for _, name := range candidates {
		if name == usual {
			continue
		}
		sc, n := score(name)
		if n >= enough && sc >= base+margin && sc > best {
			pick, best = name, sc
		}
	}
	if pick == usual {
		return usual, ""
	}
	return pick, "learned: " + pick + " has done better than " + usual + " on " + string(kind) + " messages"
}

// Explore occasionally suggests a candidate that has little evidence yet, so a good profile is not passed over only
// because it was never tried. It only does so for low-stakes kinds and rarely; the caller decides which kinds those are.
func (s *Store) Explore(kind Kind, candidates []string, usual string, chance float64) string {
	if s == nil || len(candidates) < 2 || rand.Float64() >= chance {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	var fresh []string
	for _, name := range candidates {
		if name == usual {
			continue
		}
		if c := s.data[string(kind)][name]; c == nil {
			fresh = append(fresh, name)
		} else if _, n := c.Score(); n < 3 {
			fresh = append(fresh, name)
		}
	}
	if len(fresh) == 0 {
		return ""
	}
	return fresh[rand.IntN(len(fresh))]
}

// Row is one line of what has been learned.
type Row struct {
	Kind    string  `json:"kind"`
	Profile string  `json:"profile"`
	Count           // the raw counts
	Score   float64 `json:"score"`
	Seen    float64 `json:"seen"`
}

// Rows lists everything learned, best first within each kind.
func (s *Store) Rows() []Row {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	var out []Row
	for kind, ps := range s.data {
		for name, c := range ps {
			sc, n := c.Score()
			out = append(out, Row{Kind: kind, Profile: name, Count: *c, Score: sc, Seen: n})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Score > out[j].Score
	})
	return out
}

// Reset forgets everything learned.
func (s *Store) Reset() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded, s.data = true, map[string]map[string]*Count{}
	if s.Path != "" {
		if err := os.Remove(s.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
