package httpx

import "testing"

func TestPlainKeepsTextAndDropsWhatCouldDisguiseIt(t *testing.T) {
	for _, test := range []struct{ name, in, want string }{
		{"ordinary text is untouched", "Ada’s MacBook Pro", "Ada’s MacBook Pro"},
		{"a right-to-left override can't reverse what follows", "Studio‮gpj.exe", "Studiogpj.exe"},
		{"zero-width characters can't make two names look alike", "Stu​dio", "Studio"},
		{"line breaks and escapes can't start a new line", "diarizing\n\x1b[2Jdone\r", "diarizing [2Jdone"},
		{"whitespace collapses", "  two   spaces\tand a tab ", "two spaces and a tab"},
		{"invalid UTF-8 shows as a replacement character", "bad \xff byte", "bad � byte"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Plain(test.in); got != test.want {
				t.Errorf("Plain(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}
