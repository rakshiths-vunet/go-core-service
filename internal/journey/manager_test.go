package journey

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWithDefaults(t *testing.T) {
	got := withDefaults(Params{})
	want := Params{TargetURL: defaultTargetURL, ClickIntervalMS: defaultClickInterval, Iterations: defaultIterations}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}

	got = withDefaults(Params{TargetURL: "http://x/", ClickIntervalMS: 500, Iterations: 3})
	want = Params{TargetURL: "http://x/", ClickIntervalMS: 500, Iterations: 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestLastLogEntry(t *testing.T) {
	dir := t.TempDir()
	m := New()
	m.logFile = filepath.Join(dir, "journey.log")
	m.pidFile = filepath.Join(dir, "journey.pid")
	m.outFile = filepath.Join(dir, "journey.out")

	content := `{"ts":"2026-08-11T10:15:46.828Z","level":"info","msg":"journey_starting","targetUrl":"http://10.1.92.192:8081/","clickIntervalMs":2000,"iterations":"infinite"}
{"ts":"2026-08-11T10:15:48.831Z","level":"info","msg":"trace_test_clicked","iteration":566,"latencyMs":138,"httpStatus":200,"result":"{}"}
garbage line
`
	if err := os.WriteFile(m.logFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	e := m.lastLogEntry()
	if e == nil {
		t.Fatal("expected a log entry")
	}
	if e.Iteration != 566 || e.LatencyMS != 138 || e.Msg != "trace_test_clicked" {
		t.Fatalf("unexpected entry: %+v", e)
	}
}

func TestLastLogEntryMissingFile(t *testing.T) {
	m := New()
	m.logFile = filepath.Join(t.TempDir(), "does-not-exist.log")
	if e := m.lastLogEntry(); e != nil {
		t.Fatalf("expected nil entry, got %+v", e)
	}
}

func TestLastJSONLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"only garbage", "foo\nbar\n", ""},
		{"last json wins", "{\"a\":1}\nnot json\n{\"b\":2}\n", `{"b":2}`},
		{"json with trailing newline", "{\"a\":1}\n", `{"a":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lastJSONLine([]byte(tc.in)); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
