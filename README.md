# Archive

A Go + SQLite web app for managing PDF documents.

## Features

- Username/password login with cookie sessions.
- Drag/drop PDF upload.
- Newest-first document grid with generated first-page previews when `pdftoppm` is available.
- Browser PDF viewer with stored metadata.
- SQLite FTS5 search over titles, tags, summaries, entities, keywords, and synonym terms.
- AI analysis through a configurable OpenAI-compatible chat completions endpoint.
- IMAP account settings and periodic PDF attachment import.
- Paperless-ngx import from its REST API.
- Mobile-friendly layout.
- Camera scanner page with adjustable crop polygon, multi-page capture, and PDF upload.

## Run locally

```sh
go mod tidy
go run -tags sqlite_fts5 ./cmd/archive
```

Open `http://localhost:8080`.

Default login:

- Username: `admin`
- Password: `admin`

Change the default password hash directly in SQLite before exposing the app publicly.

## Docker Compose

```sh
docker compose up --build
```

The app stores SQLite data, PDFs, and previews in the `archive-data` volume.

To use a different host port without changing tracked files, create a local `.env` file:

```sh
ARCHIVE_PORT=9090
```

## Search Concept

Each document is indexed into SQLite FTS5. During analysis the app asks AI to derive:

- a concise title,
- normalized tags,
- a dense summary,
- entities such as names, companies, dates, amounts, account numbers, and locations,
- likely user search keywords,
- synonyms and adjacent terms.

Those fields are indexed together, so searches like `bill`, `invoice`, `payment`, or vendor names can find the same document. The local query expander also adds a small built-in synonym set before querying FTS with prefix terms for fast fuzzy-ish matching.

For stronger semantic search later, add an `embeddings` table populated during AI analysis and use a vector extension or sidecar service. The current implementation deliberately avoids that extra deployment dependency.

## Paperless-ngx Import

In Settings, configure:

- Paperless-ngx URL, for example `https://paperless.example.com`
- Paperless-ngx API token

Then click `Import PDFs now`. The importer pages through `/api/documents/`, downloads each document from `/api/documents/{id}/download/`, stores it locally, and starts the normal preview and AI processing flow.

Imports are de-duplicated by Paperless document ID. If Paperless is down, the job records the error and can be retried later. If AI usage is exhausted, the PDF remains imported and the document keeps an analysis error so it can be reprocessed later.
