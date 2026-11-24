package masking

import "testing"

func TestPasswordResetEmail(t *testing.T) {
	tests := map[string]string{
		"a@example.com":              "a***@example.com",
		"ab@example.com":             "a***@example.com",
		"abc@example.com":            "a***c@example.com",
		"abcd@example.com":           "a***d@example.com",
		"alice@example.com":          "al***e@example.com",
		"张三丰@example.com":            "张***丰@example.com",
		"alice+tag@mail.example.com": "al***g@mail.example.com",
		"":                           "***@***",
		"broken":                     "***@***",
		"a@@example.com":             "***@***",
		"alice @example.com":         "***@***",
		"alice@example .com":         "***@***",
	}
	for input, want := range tests {
		if got := PasswordResetEmail(input); got != want {
			t.Errorf("PasswordResetEmail(%q) = %q, want %q", input, got, want)
		}
	}
}
