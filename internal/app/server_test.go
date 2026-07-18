package app

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

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

func TestFixEncodedFilenamesRenamesFileAndUpdatesDocument(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dataDir, "pdfs"), 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(dataDir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`create table documents (
		id integer primary key autoincrement,
		title text not null,
		original_name text not null,
		file_path text not null,
		preview_path text not null default '',
		sha256 text not null default '',
		tags text not null default '',
		summary text not null default '',
		entities text not null default '',
		keywords text not null default '',
		synonyms text not null default '',
		created_at datetime not null default current_timestamp,
		processed_at datetime,
		analysis_error text not null default ''
	)`)
	if err != nil {
		t.Fatal(err)
	}

	encodedName := "=?iso-8859-1?Q?BG_M=FCller-Nguyen.pdf?="
	id, duplicate, err := createDocument(db, "=?iso-8859-1?Q?BG_M=FCller-Nguyen", encodedName, "", "test-hash")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate {
		t.Fatal("unexpected duplicate")
	}
	oldRel := filepath.Join("pdfs", "1-"+encodedName)
	if err := os.WriteFile(filepath.Join(dataDir, oldRel), []byte("%PDF-1.4"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update documents set file_path = ? where id = ?`, oldRel, id); err != nil {
		t.Fatal(err)
	}

	server := &Server{cfg: Config{DataDir: dataDir}, db: db}
	stats, err := server.fixEncodedFilenames()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Fixed != 1 || stats.Skipped != 0 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want 1 fixed and no skipped/failed", stats)
	}
	doc, err := getDocument(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if doc.OriginalName != "BG Müller-Nguyen.pdf" {
		t.Fatalf("OriginalName = %q", doc.OriginalName)
	}
	if doc.Title != "BG Müller-Nguyen" {
		t.Fatalf("Title = %q", doc.Title)
	}
	wantRel := filepath.Join("pdfs", "1-BG Müller-Nguyen.pdf")
	if doc.FilePath != wantRel {
		t.Fatalf("FilePath = %q, want %q", doc.FilePath, wantRel)
	}
	if _, err := os.Stat(filepath.Join(dataDir, wantRel)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, oldRel)); !os.IsNotExist(err) {
		t.Fatalf("old file still exists or stat failed unexpectedly: %v", err)
	}
}

func TestFixEncodedFilenamesUsesStoredPathWhenOriginalNameIsAlreadyDecoded(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dataDir, "pdfs"), 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(dataDir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`create table documents (
		id integer primary key autoincrement,
		title text not null,
		original_name text not null,
		file_path text not null,
		preview_path text not null default '',
		sha256 text not null default '',
		tags text not null default '',
		summary text not null default '',
		entities text not null default '',
		keywords text not null default '',
		synonyms text not null default '',
		created_at datetime not null default current_timestamp,
		processed_at datetime,
		analysis_error text not null default ''
	)`)
	if err != nil {
		t.Fatal(err)
	}

	encodedName := "=?iso-8859-1?Q?BG_M=FCller-Nguyen.pdf?="
	id, duplicate, err := createDocument(db, "Custom title", "BG Müller-Nguyen.pdf", "", "test-hash")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate {
		t.Fatal("unexpected duplicate")
	}
	oldRel := filepath.Join("pdfs", "1-"+encodedName)
	if err := os.WriteFile(filepath.Join(dataDir, oldRel), []byte("%PDF-1.4"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update documents set file_path = ? where id = ?`, oldRel, id); err != nil {
		t.Fatal(err)
	}

	server := &Server{cfg: Config{DataDir: dataDir}, db: db}
	stats, err := server.fixEncodedFilenames()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Fixed != 1 || stats.Skipped != 0 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want 1 fixed and no skipped/failed", stats)
	}
	doc, err := getDocument(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "Custom title" {
		t.Fatalf("Title = %q", doc.Title)
	}
	wantRel := filepath.Join("pdfs", "1-BG Müller-Nguyen.pdf")
	if doc.FilePath != wantRel {
		t.Fatalf("FilePath = %q, want %q", doc.FilePath, wantRel)
	}
}
