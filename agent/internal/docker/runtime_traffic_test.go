package docker

import (
	"strings"
	"testing"
	"time"
)

func TestRawLineWriterEmitsWholeLinesWhateverTheChunks(t *testing.T) {
	var got []string
	w := &rawLineWriter{emit: func(line []byte) { got = append(got, string(line)) }}
	// One line in one write, two in one, one across three, and one that is
	// never finished.
	for _, chunk := range []string{"first\n", "second\nthird\n", "fou", "r", "th\nfifth\nsix"} {
		if n, err := w.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	if strings.Join(got, "|") != "first|second|third|fourth|fifth" {
		t.Errorf("lines = %q", got)
	}
}

func TestSinceParamKeepsTheFractionOfTheSecond(t *testing.T) {
	at := time.Unix(1791031657, 24695000)
	if got := sinceParam(at); got != "1791031657.024695000" {
		t.Errorf("since = %q, want seconds and nine digits of nanoseconds", got)
	}
}
