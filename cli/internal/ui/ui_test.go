package ui

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestUI() (*UI, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return New(&out, &errOut, func(string) string { return "" }), &out, &errOut
}

func TestPipedOutputIsPlain(t *testing.T) {
	u, out, errOut := newTestUI()
	if u.IsTerminal() {
		t.Fatal("a buffer is not a terminal")
	}

	u.Progress("Pulling image")
	u.Success("Pulled %s", "nginx")
	u.Failure("Deployment failed")
	u.Warn("careful")
	u.Done()

	if got := out.String(); got != "✓ Pulled nginx\n✗ Deployment failed\n" {
		t.Errorf("stdout = %q", got)
	}
	if got := errOut.String(); got != "! careful\n" {
		t.Errorf("warnings belong on stderr, got %q", got)
	}
	if u.Styled(Red, "x") != "x" {
		t.Error("no escape codes when piped")
	}
}

func TestStyledAndProgressOnATerminal(t *testing.T) {
	var out bytes.Buffer
	u := &UI{out: &out, err: &out, color: true, tty: true}

	if got := u.Styled(Green, "ok"); got != "\x1b[32mok\x1b[0m" {
		t.Errorf("Styled = %q", got)
	}
	if got := u.Styled(Plain, "ok"); got != "ok" {
		t.Errorf("Plain must add nothing, got %q", got)
	}

	u.Progress("Pulling image")
	u.Println("done")
	// The progress line is erased (\r + clear-to-end-of-line) before "done".
	if got := out.String(); !strings.HasSuffix(got, "\r\x1b[Kdone\n") || !strings.Contains(got, "Pulling image") {
		t.Errorf("output = %q", got)
	}
}

func TestNoteGoesToStandardErrorAndClearsTheProgressLine(t *testing.T) {
	var out, errOut bytes.Buffer
	u := Terminal(&out, &errOut)
	if !u.IsTerminal() || !u.Watched() {
		t.Fatal("Terminal must behave as a terminal")
	}
	u.Progress("Waiting")
	u.Note("Following %s", "my-api")
	if got := errOut.String(); got != "Following my-api\n" {
		t.Errorf("stderr = %q", got)
	}
	if got := out.String(); got != "… Waiting\r\x1b[K" {
		t.Errorf("stdout = %q", got)
	}
}

func TestAPrefixedShareOfATerminalIsWatchedButHasNoProgressLine(t *testing.T) {
	var out bytes.Buffer
	p := Terminal(&out, &out).Prefixed("api  ")
	if p.IsTerminal() || !p.Watched() {
		t.Errorf("IsTerminal = %v, Watched = %v", p.IsTerminal(), p.Watched())
	}
	piped, _, _ := newTestUI()
	if piped.Watched() || piped.Prefixed("api  ").Watched() {
		t.Error("nobody watches a pipe")
	}
	if piped.Width() != 80 {
		t.Errorf("Width = %d, want 80 when it cannot be known", piped.Width())
	}
}

func TestTableAlignment(t *testing.T) {
	u, out, _ := newTestUI()
	u.Table([]string{"NAME", "STATUS", ""}, [][]Cell{
		{C("my-api"), {Text: "RUNNING", Style: Green}, C("")},
		{C("a-much-longer-name"), C(""), C("note")},
	})
	want := "" +
		"NAME                 STATUS\n" +
		"my-api               RUNNING\n" +
		"a-much-longer-name   -         note\n" // STATUS is 7 wide + 3 of gutter
	if got := out.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestTableWidthIgnoresEscapeCodesAndCountsRunes(t *testing.T) {
	var out bytes.Buffer
	u := &UI{out: &out, err: &out, color: true}
	u.Table([]string{"A", "B"}, [][]Cell{
		{{Text: "ü", Style: Green}, C("x")},
		{C("long"), C("y")},
	})
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	strip := strings.NewReplacer("\x1b[32m", "", "\x1b[2m", "", "\x1b[0m", "")
	x := strings.Index(strip.Replace(lines[1]), "x") - (len("ü") - 1) // byte offset → column
	y := strings.Index(strip.Replace(lines[2]), "y")
	if x != y {
		t.Errorf("columns misaligned (x at %d, y at %d):\n%s", x, y, out.String())
	}
}

func TestFields(t *testing.T) {
	u, out, _ := newTestUI()
	u.Fields([][2]string{{"Version", "1.4.2"}, {"Image", "nginx"}})
	if got := out.String(); got != "Version   1.4.2\nImage     nginx\n" {
		t.Errorf("got %q", got)
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	tests := map[time.Duration]string{
		-5 * time.Minute:    "just now", // server clock ahead of ours
		3 * time.Second:     "just now",
		42 * time.Second:    "42s ago",
		5 * time.Minute:     "5m ago",
		3 * time.Hour:       "3h ago",
		50 * time.Hour:      "2d ago",
		29 * 24 * time.Hour: "29d ago",
	}
	for ago, want := range tests {
		if got := RelativeTime(now.Add(-ago), now); got != want {
			t.Errorf("RelativeTime(-%s) = %q, want %q", ago, got, want)
		}
	}
	if got := RelativeTime(now.Add(-90*24*time.Hour), now); !strings.HasPrefix(got, "2025-12-") {
		t.Errorf("old timestamps should be dates, got %q", got)
	}
}

func TestDuration(t *testing.T) {
	tests := map[time.Duration]string{
		850 * time.Millisecond:  "850ms",
		4200 * time.Millisecond: "4.2s",
		72 * time.Second:        "1m12s",
	}
	for d, want := range tests {
		if got := Duration(d); got != want {
			t.Errorf("Duration(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestPrefixedLinesNeverInterleave(t *testing.T) {
	u, out, errOut := newTestUI()
	api, web := u.Prefixed("api  "), u.Prefixed("web  ")

	api.Success("Pulled image")
	api.Println()                       // dropped: between other applications' lines it separates nothing
	api.Printf("%d/%d replicas ", 1, 2) // a line written in two parts stays one line
	api.Printf("healthy\n")
	web.Warn("careful")
	api.Progress("never shown")
	u.Println("Skipped worker: api did not deploy")

	if got := out.String(); got != "api  ✓ Pulled image\napi  1/2 replicas healthy\nSkipped worker: api did not deploy\n" {
		t.Errorf("stdout = %q", got)
	}
	if got := errOut.String(); got != "web  ! careful\n" {
		t.Errorf("stderr = %q", got)
	}

	// Two applications narrating at once: every line reaches the buffer whole
	// and under its own prefix.
	out.Reset()
	var wg sync.WaitGroup
	for _, p := range []*UI{api, web} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				p.Success("step %d of a fairly long line that is worth interleaving", i)
			}
		}()
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 400 {
		t.Fatalf("got %d lines, want 400", len(lines))
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "api  ✓ step ") && !strings.HasPrefix(line, "web  ✓ step ") || !strings.HasSuffix(line, "interleaving") {
			t.Fatalf("interleaved line: %q", line)
		}
	}
}
