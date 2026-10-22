package testreport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"syscall"
	"testing"
	"time"
)

// Report title written at the top of test_cases.md.
const Title = "Notifications test cases"

type Case struct {
	Title  string `json:"title"`
	Test   string `json:"test"`
	Result string `json:"result"`
}

// Done records this test under Title. Call it with defer at the start of a test
// so the single report file updates on every run.
func Done(t *testing.T, title string) {
	t.Helper()
	result := "pass"
	if t.Failed() {
		result = "fail"
	}
	if err := upsert(title, t.Name(), result); err != nil {
		t.Logf("test report: %v", err)
	}
}

func upsert(title, name, result string) error {
	path, err := reportPath()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	raw, _ := os.ReadFile(path)
	cases := parseCases(raw)
	next := Case{Title: title, Test: name, Result: result}
	replaced := false
	for i := range cases {
		if cases[i].Test == name {
			cases[i] = next
			replaced = true
			break
		}
	}
	if !replaced {
		cases = append(cases, next)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].Test < cases[j].Test })
	body := render(cases)
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	_, err = f.WriteString(body)
	return err
}

func reportPath() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "test_cases.md"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

var casesBlock = regexp.MustCompile(`(?s)<!-- cases\n(.*)\n-->`)

func parseCases(raw []byte) []Case {
	m := casesBlock.FindSubmatch(raw)
	if m == nil {
		return nil
	}
	var cases []Case
	_ = json.Unmarshal(m[1], &cases)
	return cases
}

func render(cases []Case) string {
	encoded, _ := json.MarshalIndent(cases, "", "  ")
	out := "# " + Title + "\n\n"
	out += "Updated: " + time.Now().Format(time.RFC3339) + "\n\n"
	out += "| Title | Test | Result |\n|---|---|---|\n"
	for _, c := range cases {
		out += "| " + c.Title + " | `" + c.Test + "` | " + c.Result + " |\n"
	}
	out += "\n<!-- cases\n" + string(encoded) + "\n-->\n"
	return out
}
