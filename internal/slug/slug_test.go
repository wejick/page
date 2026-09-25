package slug

import (
	"errors"
	"testing"
)

func TestSanitize(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{name: "spec scenario messy identifier", in: "  Landing Page! ", want: "landing-page"},
		{name: "spec scenario unsalvageable", in: "???", wantErr: ErrInvalidIdentifier},
		{name: "already clean", in: "landing-page", want: "landing-page"},
		{name: "uppercase and digits kept", in: "Pricing V2 2024", want: "pricing-v2-2024"},
		{name: "runs of junk collapse to one dash", in: "a  !! b", want: "a-b"},
		{name: "leading trailing dashes trimmed", in: "--x--", want: "x"},
		{name: "length capped at 64", in: strings_Seed(100), want: strings_Seed(100)[:64]},
		{name: "empty after trim", in: "   ", wantErr: ErrInvalidIdentifier},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Sanitize(tt.in)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Sanitize(%q) err = %v, want %v", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Sanitize(%q) err = %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("Sanitize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidateReserved(t *testing.T) {
	for _, id := range []string{"api", "a", "p", "ui", "www", "assets", "cdn", "static", "healthz"} {
		if err := Validate(id); !errors.Is(err, ErrReserved) {
			t.Fatalf("Validate(%q) = %v, want ErrReserved", id, err)
		}
	}
	for _, id := range []string{"landing-page", "api-docs", "assets-team", "healthz2"} {
		if err := Validate(id); err != nil {
			t.Fatalf("Validate(%q) = %v, want nil", id, err)
		}
	}
}

func TestCompose(t *testing.T) {
	if got := Compose("landing-page", 7); got != "landing-page-7" {
		t.Fatalf("Compose = %q", got)
	}
	// Numbered identifier with appended code is unambiguous (D6).
	if got := Compose("landing-page-1", 2); got != "landing-page-1-2" {
		t.Fatalf("Compose = %q", got)
	}
}

func strings_Seed(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return string(b)
}
