// Package prompts holds the skills' prompt templates, embedded from this
// directory. Each skill version has one text/template file named
// <skill>.v<version>.tmpl that defines two templates: "instructions", the
// skill's layer of the system prompt, and "user", the user message. Both
// execute with the data the skill derives from its input.
//
// A template never changes under a released version: checksums.txt records
// the SHA-256 of every template, and a test fails when a template no longer
// matches it. Changing a prompt means bumping the skill's version, which
// renames its template file.
package prompts

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"text/template"
)

//go:embed *.tmpl
var files embed.FS

// fileName matches a template file name and captures the skill name and
// version.
var fileName = regexp.MustCompile(`^([a-z][a-z0-9_]*)\.v([1-9][0-9]*)\.tmpl$`)

// templates maps a template file name to its parsed template. A bad
// template stops the service at start.
var templates = mustParse(files)

func mustParse(fsys fs.FS) map[string]*template.Template {
	t, err := parse(fsys)
	if err != nil {
		panic(err)
	}
	return t
}

// parse parses every template in fsys, each on its own so that every skill
// can define "instructions" and "user".
func parse(fsys fs.FS) (map[string]*template.Template, error) {
	names, err := fs.Glob(fsys, "*.tmpl")
	if err != nil {
		return nil, err
	}
	out := make(map[string]*template.Template, len(names))
	for _, name := range names {
		if !fileName.MatchString(name) {
			return nil, fmt.Errorf("prompts: %s: file name is not <skill>.v<version>.tmpl", name)
		}
		t, err := template.New(name).Option("missingkey=error").ParseFS(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("prompts: %w", err)
		}
		for _, part := range []string{"instructions", "user"} {
			if t.Lookup(part) == nil {
				return nil, fmt.Errorf("prompts: %s: does not define %q", name, part)
			}
		}
		out[name] = t
	}
	return out, nil
}

// File is the name of the template file for version of skill.
func File(skill string, version int) string {
	return skill + ".v" + strconv.Itoa(version) + ".tmpl"
}

// Render executes the template for version of skill with data and returns
// its instructions and user message, trimmed of surrounding white space.
// Either being empty is an error.
func Render(skill string, version int, data any) (instructions, user string, err error) {
	name := File(skill, version)
	t, ok := templates[name]
	if !ok {
		return "", "", fmt.Errorf("prompts: no template %s for skill %s v%d", name, skill, version)
	}
	if instructions, err = execute(t, "instructions", data); err != nil {
		return "", "", err
	}
	if user, err = execute(t, "user", data); err != nil {
		return "", "", err
	}
	return instructions, user, nil
}

func execute(t *template.Template, part string, data any) (string, error) {
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, part, data); err != nil {
		return "", fmt.Errorf("prompts: %w", err)
	}
	s := strings.TrimSpace(b.String())
	if s == "" {
		return "", fmt.Errorf("prompts: %s: %q rendered empty", t.Name(), part)
	}
	return s, nil
}
