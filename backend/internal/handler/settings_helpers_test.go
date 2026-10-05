package handler

import "testing"

func TestMaskSecret(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "short", in: "12345678", want: "****"},
		{name: "long", in: "abcdefghijklmnop", want: "abcd****mnop"},
		{name: "already masked", in: "abcd****mnop", want: "abcd****mnop"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maskSecret(tt.in); got != tt.want {
				t.Fatalf("maskSecret(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsMaskedSecret(t *testing.T) {
	if !isMaskedSecret("  abcd****mnop  ") {
		t.Fatal("expected masked secret to be detected")
	}
	if isMaskedSecret("abcdefghijklmnop") {
		t.Fatal("did not expect a normal secret to be detected as masked")
	}
}
