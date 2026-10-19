package main

import (
	"io"
	"strings"
	"sync"
	"text/template"

	"github.com/gastownhall/gascity/internal/fsys"
)

// promptFragmentCache remembers the shared prompt fragments renderPrompt
// parses, for one build. Every agent of a pack loads the same fragment dirs
// (see promptFragmentDirs), so without it a build of 164 agents parsed the
// same files 164 times: a third of resolveTemplate's CPU (vn-qn7f49h).
//
// It holds one parsed set per ordered dir list, and every prefix of that list
// on the way, so agents that share their pack dirs and differ only in their
// own prompt's dirs parse the shared part once. A render gets a Clone, never
// the cached set, so its own body and funcs stay out of every other render.
//
// Keep it per build, like templateResolveCost: an edited fragment must reach
// the next tick. It reads through whatever fsys.FS the caller passes, so one
// cache must only ever see one FS. A nil cache parses fresh on every call,
// which is what one-off renders (gc prime) want.
type promptFragmentCache struct {
	mu   sync.Mutex
	sets map[string]*template.Template // key: the ordered dir list, NUL-joined
}

func newPromptFragmentCache() *promptFragmentCache {
	return &promptFragmentCache{sets: make(map[string]*template.Template)}
}

// fragmentSet returns a template named "prompt" holding every fragment under
// dirs, loaded in order (a later dir wins a name collision). The caller owns
// the result: it may parse into it and must rebind its funcs with Funcs
// before executing, because the set was parsed with placeholder funcs.
func (c *promptFragmentCache) fragmentSet(fs fsys.FS, dirs []string, stderr io.Writer) (*template.Template, error) {
	if c == nil {
		t := newPromptFragmentSet()
		for _, dir := range dirs {
			loadSharedTemplates(fs, t, dir, stderr)
		}
		return t, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t, err := c.setLocked(fs, dirs, stderr)
	if err != nil {
		return nil, err
	}
	return t.Clone()
}

// setLocked returns the cached set for dirs, building it from the cached set
// for dirs minus its last entry. Nothing outside setLocked may touch a cached
// set except to Clone it.
func (c *promptFragmentCache) setLocked(fs fsys.FS, dirs []string, stderr io.Writer) (*template.Template, error) {
	key := strings.Join(dirs, "\x00")
	if t, ok := c.sets[key]; ok {
		return t, nil
	}
	var t *template.Template
	if len(dirs) == 0 {
		t = newPromptFragmentSet()
	} else {
		parent, err := c.setLocked(fs, dirs[:len(dirs)-1], stderr)
		if err != nil {
			return nil, err
		}
		if t, err = parent.Clone(); err != nil {
			return nil, err
		}
		loadSharedTemplates(fs, t, dirs[len(dirs)-1], stderr)
	}
	c.sets[key] = t
	return t, nil
}

// newPromptFragmentSet is the empty set every fragment set starts from. The
// funcs are placeholders: text/template needs a func's name at parse time but
// looks the func up again at execute time, so each render binds its own
// session and templateFirst (which close over that render's store and
// template) on its clone.
func newPromptFragmentSet() *template.Template {
	return template.New("prompt").
		Funcs(promptFuncMap("", "", nil, func() *template.Template { return nil })).
		Option("missingkey=zero")
}
