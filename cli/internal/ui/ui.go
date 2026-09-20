// Package ui renders deployctl's terminal output: status lines, tables,
// colors. Output degrades gracefully when it is piped or colors are disabled.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
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

	transient bool // a progress line is currently displayed
}

// New creates a UI writing to out and err. Styling is enabled only when out
// is a terminal that understands it and the user has not opted out
// (https://no-color.org).
func New(out, err io.Writer, getenv func(string) string) *UI {
	u := &UI{out: out, err: err}
	if f, ok := out.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		u.tty = enableVirtualTerminal(f)
		u.color = u.tty && getenv("NO_COLOR") == "" && getenv("TERM") != "dumb"
	}
	return u
}

// IsTerminal reports whether output goes to an interactive terminal.
func (u *UI) IsTerminal() bool { return u.tty }

func (u *UI) Styled(s Style, text string) string {
	if !u.color || s == Plain {
		return text
	}
	return "\x1b[" + string(s) + "m" + text + "\x1b[0m"
}

// Println writes a line to standard output.
func (u *UI) Println(args ...any) {
	u.clearTransient()
	fmt.Fprintln(u.out, args...)
}

func (u *UI) Printf(format string, args ...any) {
	u.clearTransient()
	fmt.Fprintf(u.out, format, args...)
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
	u.clearTransient()
	fmt.Fprintln(u.err, u.Styled(Yellow, "!")+" "+fmt.Sprintf(format, args...))
}

// Progress shows what is happening right now on a line that the next output
// replaces. When output is not a terminal it prints nothing: logs should hold
// results, not animation frames.
func (u *UI) Progress(format string, args ...any) {
	if !u.tty {
		return
	}
	u.clearTransient()
	fmt.Fprint(u.out, u.Styled(Dim, "… "+fmt.Sprintf(format, args...)))
	u.transient = true
}

func (u *UI) clearTransient() {
	if u.transient {
		fmt.Fprint(u.out, "\r\x1b[K")
		u.transient = false
	}
}

// Done clears any progress line. Call it before handing the terminal back.
func (u *UI) Done() { u.clearTransient() }

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
