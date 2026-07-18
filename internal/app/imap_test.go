package app

import "testing"

func TestDecodeMIMEFilename(t *testing.T) {
	got := decodeMIMEFilename("=?iso-8859-1?Q?BG_M=FCller-Nguyen.pdf?=")
	want := "BG Müller-Nguyen.pdf"
	if got != want {
		t.Fatalf("decodeMIMEFilename() = %q, want %q", got, want)
	}
}

func TestDecodeMIMEFilenameKeepsPlainFilename(t *testing.T) {
	got := decodeMIMEFilename("invoice-2026.pdf")
	want := "invoice-2026.pdf"
	if got != want {
		t.Fatalf("decodeMIMEFilename() = %q, want %q", got, want)
	}
}
