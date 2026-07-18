package app

import "testing"

func TestFileURLPathEscapesSegments(t *testing.T) {
	got := fileURL(`pdfs/12-a #b?c%ä.pdf`)
	want := "/files/pdfs/12-a%20%23b%3Fc%25%C3%A4.pdf"
	if got != want {
		t.Fatalf("fileURL() = %q, want %q", got, want)
	}
}

func TestFileURLPreservesPathSeparators(t *testing.T) {
	got := fileURL(`previews\12.png`)
	want := "/files/previews/12.png"
	if got != want {
		t.Fatalf("fileURL() = %q, want %q", got, want)
	}
}
