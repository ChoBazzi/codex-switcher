package proxy

import (
	"strings"
	"testing"
)

func TestCompletionGate(t *testing.T) {
	first := "data: {\"type\":\"response.created\"}\r\n\r\n"
	last := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\"}}\r\n\r\n"
	for _, size := range []int{1, 7, len(first + last)} {
		g, o := completionGate{}, eventObserver{strict: true}
		input, output := first+last, ""
		for len(input) > 0 {
			n := min(size, len(input))
			b, ok := g.feed([]byte(input[:n]), &o)
			if !ok {
				t.Fatal("valid stream rejected")
			}
			output += string(b)
			input = input[n:]
		}
		if output != first || string(g.terminal) != last || !o.completed {
			t.Fatal("terminal bytes released or changed")
		}
	}
	for _, tail := range []string{last, "data: {\"type\":\"response.failed\"}\n\n", strings.Repeat("x", 1<<20)} {
		g, o := completionGate{}, eventObserver{strict: true}
		if _, ok := g.feed([]byte(last+tail), &o); ok {
			t.Fatal("bad trailing stream accepted")
		}
	}
}
