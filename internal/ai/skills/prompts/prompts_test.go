package prompts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// checkChecksums compares every template in fsys with the checksums in
// manifest and returns each mismatch, naming the skill and version.
func checkChecksums(fsys fs.FS, manifest string) []string {
	var problems []string
	recorded := map[string]string{} // template file name -> checksum
	for i, line := range strings.Split(manifest, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 || !fileName.MatchString(f[0]+"."+f[1]+".tmpl") {
			problems = append(problems, fmt.Sprintf("checksums.txt line %d: want \"<skill> v<version> <sha256>\", got %q", i+1, line))
			continue
		}
		name := f[0] + "." + f[1] + ".tmpl"
		if _, dup := recorded[name]; dup {
			problems = append(problems, fmt.Sprintf("skill %s %s: checksums.txt records it twice", f[0], f[1]))
			continue
		}
		recorded[name] = f[2]
	}

	names, err := fs.Glob(fsys, "*.tmpl")
	if err != nil {
		return append(problems, err.Error())
	}
	for _, name := range names {
		m := fileName.FindStringSubmatch(name)
		if m == nil {
			problems = append(problems, fmt.Sprintf("%s: file name is not <skill>.v<version>.tmpl", name))
			continue
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		want, ok := recorded[name]
		delete(recorded, name)
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("skill %s v%s: %s has no recorded checksum; add \"%s v%s %s\" to checksums.txt",
				m[1], m[2], name, m[1], m[2], got))
		case got != want:
			problems = append(problems, fmt.Sprintf("skill %s v%s: %s changed without a version bump (sha256 %s, recorded %s); bump the skill's version and rename its template",
				m[1], m[2], name, got, want))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(recorded)) {
		m := fileName.FindStringSubmatch(name)
		problems = append(problems, fmt.Sprintf("skill %s v%s: checksums.txt records %s but there is no such template; remove the line", m[1], m[2], name))
	}
	return problems
}

func TestTemplateChecksums(t *testing.T) {
	manifest, err := os.ReadFile("checksums.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range checkChecksums(files, string(manifest)) {
		t.Error(p)
	}
}

const echoV1 = `{{define "instructions"}}Repeat words.{{end}}{{define "user"}}Repeat: {{.Word}}{{end}}`

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestCheckChecksums(t *testing.T) {
	changed := echoV1 + "\n"
	for name, tc := range map[string]struct {
		fsys     fstest.MapFS
		manifest string
		want     []string
	}{
		"match": {
			fsys:     fstest.MapFS{"echo.v1.tmpl": {Data: []byte(echoV1)}},
			manifest: "# comment\necho v1 " + sum(echoV1) + "\n",
		},
		"changed without bump": {
			fsys:     fstest.MapFS{"echo.v1.tmpl": {Data: []byte(changed)}},
			manifest: "echo v1 " + sum(echoV1),
			want: []string{"skill echo v1: echo.v1.tmpl changed without a version bump (sha256 " + sum(changed) +
				", recorded " + sum(echoV1) + "); bump the skill's version and rename its template"},
		},
		"bumped but not recorded": {
			fsys:     fstest.MapFS{"echo.v2.tmpl": {Data: []byte(changed)}},
			manifest: "echo v1 " + sum(echoV1),
			want: []string{
				`skill echo v2: echo.v2.tmpl has no recorded checksum; add "echo v2 ` + sum(changed) + `" to checksums.txt`,
				"skill echo v1: checksums.txt records echo.v1.tmpl but there is no such template; remove the line",
			},
		},
		"duplicate and malformed": {
			fsys:     fstest.MapFS{"echo.v1.tmpl": {Data: []byte(echoV1)}},
			manifest: "echo v1 " + sum(echoV1) + "\necho v1 " + sum(echoV1) + "\necho 1 abc\necho v01 abc",
			want: []string{
				"skill echo v1: checksums.txt records it twice",
				`checksums.txt line 3: want "<skill> v<version> <sha256>", got "echo 1 abc"`,
				`checksums.txt line 4: want "<skill> v<version> <sha256>", got "echo v01 abc"`,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := checkChecksums(tc.fsys, tc.manifest); !slices.Equal(got, tc.want) {
				t.Fatalf("problems =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

func TestRender(t *testing.T) {
	instructions, user, err := Render("echo", 1, struct{ Word string }{"Taco"})
	if err != nil {
		t.Fatal(err)
	}
	if instructions != "You repeat words. Reply with the word you are given, exactly as written but in lower case." {
		t.Fatalf("instructions = %q", instructions)
	}
	if user != "Repeat: Taco" {
		t.Fatalf("user = %q", user)
	}
}

func TestRenderErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		skill   string
		version int
		data    any
		want    string
	}{
		"unknown version": {"echo", 2, struct{ Word string }{"x"}, "no template echo.v2.tmpl for skill echo v2"},
		"unknown skill":   {"nope", 1, nil, "no template nope.v1.tmpl"},
		"missing field":   {"echo", 1, struct{}{}, "can't evaluate field Word"},
		"missing key":     {"echo", 1, map[string]string{}, "map has no entry for key"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := Render(tc.skill, tc.version, tc.data)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestRenderedEmptyIsError(t *testing.T) {
	ts, err := parse(fstest.MapFS{"blank.v1.tmpl": {Data: []byte(`{{define "instructions"}}x{{end}}{{define "user"}} {{.}} {{end}}`)}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = execute(ts["blank.v1.tmpl"], "user", "\n")
	if err == nil || !strings.Contains(err.Error(), `blank.v1.tmpl: "user" rendered empty`) {
		t.Fatalf("err = %v, want rendered empty", err)
	}
}

func TestParseRejects(t *testing.T) {
	for name, tc := range map[string]struct {
		fsys fstest.MapFS
		want string
	}{
		"bad name":        {fstest.MapFS{"Echo.tmpl": {Data: []byte(echoV1)}}, "file name is not"},
		"version zero":    {fstest.MapFS{"echo.v0.tmpl": {Data: []byte(echoV1)}}, "file name is not"},
		"no instructions": {fstest.MapFS{"echo.v1.tmpl": {Data: []byte(`{{define "user"}}u{{end}}`)}}, `does not define "instructions"`},
		"no user":         {fstest.MapFS{"echo.v1.tmpl": {Data: []byte(`{{define "instructions"}}i{{end}}`)}}, `does not define "user"`},
		"syntax":          {fstest.MapFS{"echo.v1.tmpl": {Data: []byte(`{{define "user"}}`)}}, "unexpected EOF"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parse(tc.fsys)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}
