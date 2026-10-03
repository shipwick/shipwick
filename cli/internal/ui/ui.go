// Package ui renders shipwick's terminal output: status lines, tables,
// colors. Output degrades gracefully when it is piped or colors are disabled.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

// Style is an ANSI style; the zero value is "no styling".
type Style string

const (
	Plain  Style = ""
	Bold   Style = "1"
	Dim    Style = "2"
	Red    Style = "31"
	Green  Style = "32"
	Yellow Style = "33"
	Cyan   Style = "36"
)

type UI struct {
	out io.Writer
	err io.Writer
	// color enables ANSI styling; tty additionally enables the transient
	// progress line, which needs cursor control.
	color bool
	tty   bool
	// watched says a person reads this as it is written: a terminal, or one
	// application's share of it (see Prefixed).
	watched bool

	// mu serialises writes: several deployments narrating at once (see
	// Prefixed) must not interleave inside a line.
	mu        sync.Mutex
	transient bool // a progress line is currently displayed
}

// New creates a UI writing to out and err. Styling is enabled only when out
// is a terminal that understands it and the user has not opted out
// (https://no-color.org).
func New(out, err io.Writer, getenv func(string) string) *UI {
	u := &UI{out: out, err: err}
	if f, ok := out.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		u.tty = enableVirtualTerminal(f)
		u.watched = true
		u.color = u.tty && getenv("NO_COLOR") == "" && getenv("TERM") != "dumb"
	}
	return u
}

// Terminal creates a UI that writes to out and err as to a terminal, whatever
// they are, without colors. It is how what only a terminal shows — the
// progress line — is tested through a buffer.
func Terminal(out, err io.Writer) *UI {
	return &UI{out: out, err: err, tty: true, watched: true}
}

// IsTerminal reports whether output goes to an interactive terminal.
func (u *UI) IsTerminal() bool { return u.tty }

// Watched reports whether a person reads the output as it is written: it is
// a terminal, or a Prefixed share of one. A pipeline's log is read afterwards
// and wants everything; a person wants what matters now.
func (u *UI) Watched() bool { return u.watched }

// Width is how many columns a line may take before the terminal wraps it;
// 80 when that cannot be known.
func (u *UI) Width() int {
	if f, ok := u.out.(*os.File); ok {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
			return w
		}
	}
	return 80
}

func (u *UI) Styled(s Style, text string) string {
	if !u.color || s == Plain {
		return text
	}
	return "\x1b[" + string(s) + "m" + text + "\x1b[0m"
}

// Println writes a line to standard output.
func (u *UI) Println(args ...any) {
	u.write(u.out, fmt.Sprintln(args...))
}

func (u *UI) Printf(format string, args ...any) {
	u.write(u.out, fmt.Sprintf(format, args...))
}

// write is the one place output leaves through: it takes the lock, clears
// the progress line and writes s in one call.
func (u *UI) write(w io.Writer, s string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clearTransient()
	io.WriteString(w, s)
}

// Success prints a completed step: "✓ Pulled image".
func (u *UI) Success(format string, args ...any) {
	u.Println(u.Styled(Green, "✓") + " " + fmt.Sprintf(format, args...))
}

// Failure prints a failed step: "✗ Deployment failed".
func (u *UI) Failure(format string, args ...any) {
	u.Println(u.Styled(Red, "✗") + " " + fmt.Sprintf(format, args...))
}

// Warn prints a warning to standard error, keeping standard output clean for
// scripts.
func (u *UI) Warn(format string, args ...any) {
	u.write(u.err, u.Styled(Yellow, "!")+" "+fmt.Sprintf(format, args...)+"\n")
}

// Note prints a remark about the output rather than part of it, dimmed, to
// standard error: what is piped onwards stays what the command produced.
func (u *UI) Note(format string, args ...any) {
	u.write(u.err, u.Styled(Dim, fmt.Sprintf(format, args...))+"\n")
}

// Progress shows what is happening right now on a line that the next output
// replaces. When output is not a terminal it prints nothing: logs should hold
// results, not animation frames.
func (u *UI) Progress(format string, args ...any) {
	if !u.tty {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clearTransient()
	fmt.Fprint(u.out, u.Styled(Dim, "… "+fmt.Sprintf(format, args...)))
	u.transient = true
}

// clearTransient is called with mu held.
func (u *UI) clearTransient() {
	if u.transient {
		fmt.Fprint(u.out, "\r\x1b[K")
		u.transient = false
	}
}

// Done clears any progress line. Call it before handing the terminal back.
func (u *UI) Done() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clearTransient()
}

// Prefixed returns a UI that starts every line with prefix, so that several
// operations narrating at once — one deployment per application — can share
// a terminal. Lines from different prefixed UIs never interleave, because
// each reaches the terminal as one write under the parent's lock. Empty lines
// are dropped: between other applications' lines they would separate nothing.
// The progress line is off: several spinners cannot share one line.
func (u *UI) Prefixed(prefix string) *UI {
	return &UI{
		out:     &prefixWriter{ui: u, w: u.out, prefix: prefix},
		err:     &prefixWriter{ui: u, w: u.err, prefix: prefix},
		color:   u.color,
		watched: u.watched,
	}
}

// prefixWriter belongs to one prefixed UI and hence to one goroutine; only
// the write to the parent is shared.
type prefixWriter struct {
	ui     *UI
	w      io.Writer
	prefix string
	mid    bool // the previous write ended inside a line
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	var out strings.Builder
	rest := string(b)
	for rest != "" {
		line, tail, nl := strings.Cut(rest, "\n")
		rest = tail
		if !p.mid {
			if line == "" && nl {
				continue
			}
			out.WriteString(p.prefix)
		}
		out.WriteString(line)
		if nl {
			out.WriteString("\n")
		}
		p.mid = !nl
	}
	if out.Len() > 0 {
		p.ui.write(p.w, out.String())
	}
	return len(b), nil
}

// Cell is one table cell with an optional style.
type Cell struct {
	Text  string
	Style Style
}

// C is shorthand for an unstyled cell.
func C(text string) Cell { return Cell{Text: text} }

// Table prints rows in aligned columns under dimmed headers.
func (u *UI) Table(headers []string, rows [][]Cell) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if n := utf8.RuneCountInString(cell.Text); i < len(widths) && n > widths[i] {
				widths[i] = n
			}
		}
	}

	head := make([]Cell, len(headers))
	for i, h := range headers {
		head[i] = Cell{Text: h, Style: Dim}
	}
	for r, row := range append([][]Cell{head}, rows...) {
		var b strings.Builder
		for i, cell := range row {
			text := cell.Text
			// A dash keeps empty cells visible inside the grid. Headers and
			// the trailing column (free text, e.g. an error) stay blank.
			if text == "" && r > 0 && i < len(row)-1 {
				text = "-"
			}
			b.WriteString(u.Styled(cell.Style, text))
			if i < len(row)-1 {
				// Pad after styling: escape codes must not count as width.
				b.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(text)+3))
			}
		}
		u.Println(strings.TrimRight(b.String(), " "))
	}
}

// Fields prints "label   value" pairs with aligned values.
func (u *UI) Fields(pairs [][2]string) {
	width := 0
	for _, p := range pairs {
		width = max(width, utf8.RuneCountInString(p[0]))
	}
	for _, p := range pairs {
		pad := strings.Repeat(" ", width-utf8.RuneCountInString(p[0])+3)
		u.Println(u.Styled(Dim, p[0]) + pad + p[1])
	}
}

// RelativeTime renders t relative to now: "just now", "5m ago", "3d ago".
func RelativeTime(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 0:
		return "just now" // clock skew between this machine and the server
	case d < 10*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Local().Format("2006-01-02")
}

// Duration renders a short elapsed time: "850ms", "4.2s", "1m12s".
func Duration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}
