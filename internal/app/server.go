package app

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Server struct {
	cfg              Config
	db               *sql.DB
	templates        *template.Template
	paperlessMu      sync.Mutex
	paperlessRunning bool
}

func Run(cfg Config) error {
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "pdfs"), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "previews"), 0755); err != nil {
		return err
	}
	db, err := openDB(filepath.Join(cfg.DataDir, "archive.db"))
	if err != nil {
		return err
	}
	tmpl, err := template.ParseGlob("web/templates/*.html")
	if err != nil {
		return err
	}
	s := &Server{cfg: cfg, db: db, templates: tmpl}
	go s.imapLoop()
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("web/static"))))
	mux.Handle("/files/", s.requireAuth(http.StripPrefix("/files/", http.FileServer(http.Dir(cfg.DataDir)))))
	mux.HandleFunc("/login", s.login)
	mux.HandleFunc("/logout", s.logout)
	mux.Handle("/", s.requireAuth(http.HandlerFunc(s.index)))
	mux.Handle("/documents", s.requireAuth(http.HandlerFunc(s.documents)))
	mux.Handle("/documents/", s.requireAuth(http.HandlerFunc(s.documentDetail)))
	mux.Handle("/metadata/", s.requireAuth(http.HandlerFunc(s.updateMetadata)))
	mux.Handle("/delete/", s.requireAuth(http.HandlerFunc(s.deleteDocument)))
	mux.Handle("/reprocess/", s.requireAuth(http.HandlerFunc(s.reprocessDocument)))
	mux.Handle("/upload", s.requireAuth(http.HandlerFunc(s.upload)))
	mux.Handle("/scanner", s.requireAuth(http.HandlerFunc(s.scanner)))
	mux.Handle("/settings", s.requireAuth(http.HandlerFunc(s.settings)))
	mux.Handle("/logs", s.requireAuth(http.HandlerFunc(s.logs)))
	mux.Handle("/imap", s.requireAuth(http.HandlerFunc(s.addIMAP)))
	mux.Handle("/paperless/import", s.requireAuth(http.HandlerFunc(s.importPaperless)))
	log.Printf("listening on %s", cfg.Addr)
	return http.ListenAndServe(cfg.Addr, mux)
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	docs, err := listDocuments(s.db, r.URL.Query().Get("q"), r.URL.Query().Get("tag"), 100)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	tags, _ := allTags(s.db)
	s.render(w, r, "index.html", map[string]any{
		"Docs": docs, "Tags": tags, "Q": r.URL.Query().Get("q"), "ActiveTag": r.URL.Query().Get("tag"),
	})
}

func (s *Server) documents(w http.ResponseWriter, r *http.Request) {
	docs, err := listDocuments(s.db, r.URL.Query().Get("q"), r.URL.Query().Get("tag"), 100)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(docs)
}

func (s *Server) documentDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/documents/"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	doc, err := getDocument(s.db, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, r, "document.html", doc)
}

func (s *Server) updateMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/metadata/"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}
	tags := splitCSV(r.FormValue("tags"))
	if err := updateDocumentMetadata(s.db, id, title, tags); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	addLog(s.db, "info", "document", fmt.Sprintf("Updated metadata for document %d", id), &id, nil)
	http.Redirect(w, r, fmt.Sprintf("/documents/%d", id), http.StatusSeeOther)
}

func (s *Server) deleteDocument(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/delete/"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	doc, err := getDocument(s.db, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := deleteDocument(s.db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	removeDataFile(s.cfg.DataDir, doc.FilePath)
	removeDataFile(s.cfg.DataDir, doc.PreviewPath)
	addLog(s.db, "info", "document", "Deleted document "+doc.OriginalName, &id, nil)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) reprocessDocument(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/reprocess/"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := getDocument(s.db, id); err != nil {
		http.NotFound(w, r)
		return
	}
	addLog(s.db, "info", "document", fmt.Sprintf("Queued reprocess for document %d", id), &id, nil)
	go s.processDocument(id)
	redirect := r.FormValue("redirect")
	if redirect == "" || !strings.HasPrefix(redirect, "/") || strings.HasPrefix(redirect, "//") {
		redirect = "/"
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(256 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	files := r.MultipartForm.File["pdf"]
	if len(files) == 0 {
		http.Error(w, "missing pdf", http.StatusBadRequest)
		return
	}
	var ids []int64
	for _, fh := range files {
		id, err := s.storeUploadedPDF(fh)
		if err != nil {
			addLog(s.db, "error", "upload", "PDF upload failed: "+err.Error(), nil, nil)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if id > 0 {
			ids = append(ids, id)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ids": ids})
}

func (s *Server) storeUploadedPDF(fh *multipart.FileHeader) (int64, error) {
	if !strings.HasSuffix(strings.ToLower(fh.Filename), ".pdf") {
		return 0, errors.New("only PDFs are supported")
	}
	src, err := fh.Open()
	if err != nil {
		return 0, err
	}
	defer src.Close()
	name := safeBase(fh.Filename)
	title := strings.TrimSuffix(name, filepath.Ext(name))
	tmp, err := os.CreateTemp(s.cfg.DataDir, "upload-*.pdf")
	if err != nil {
		return 0, err
	}
	tmpPath := tmp.Name()
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return 0, err
	}
	hash, err := fileSHA256(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return 0, err
	}
	id, duplicate, err := createDocument(s.db, title, name, "", hash)
	if err != nil {
		_ = os.Remove(tmpPath)
		return 0, err
	}
	if duplicate {
		_ = os.Remove(tmpPath)
		addLog(s.db, "info", "upload", "Skipped duplicate PDF "+name, nil, nil)
		return 0, nil
	}
	rel := filepath.Join("pdfs", fmt.Sprintf("%d-%s", id, name))
	dstPath := filepath.Join(s.cfg.DataDir, rel)
	if err := os.Rename(tmpPath, dstPath); err != nil {
		_ = deleteDocument(s.db, id)
		_ = os.Remove(tmpPath)
		return 0, err
	}
	_, err = s.db.Exec(`update documents set file_path = ? where id = ?`, rel, id)
	if err != nil {
		return 0, err
	}
	addLog(s.db, "info", "upload", "Uploaded PDF "+name, &id, nil)
	go s.processDocument(id)
	return id, nil
}

func (s *Server) scanner(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "scanner.html", nil)
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	entries, err := listLogs(s.db, 250)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "logs.html", map[string]any{"Logs": entries})
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		mins, _ := strconv.Atoi(r.FormValue("scan_every_mins"))
		if mins < 1 {
			mins = 15
		}
		err := saveSettings(s.db, Settings{
			AIEndpoint:        r.FormValue("ai_endpoint"),
			AIModel:           r.FormValue("ai_model"),
			AIAPIKey:          r.FormValue("ai_api_key"),
			ScanEveryMins:     mins,
			PaperlessBaseURL:  strings.TrimSpace(r.FormValue("paperless_base_url")),
			PaperlessAPIToken: strings.TrimSpace(r.FormValue("paperless_api_token")),
		})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		addLog(s.db, "info", "settings", "Saved settings", nil, nil)
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	settings, _ := getSettings(s.db)
	accounts, _ := listIMAPAccounts(s.db)
	job, _ := latestImportJob(s.db, "paperless-ngx")
	s.render(w, r, "settings.html", map[string]any{"Settings": settings, "Accounts": accounts, "PaperlessJob": job})
}

func (s *Server) importPaperless(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	settings, err := getSettings(s.db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if settings.PaperlessBaseURL == "" || settings.PaperlessAPIToken == "" {
		addLog(s.db, "error", "paperless", "Paperless import was requested without URL or API token", nil, nil)
		http.Error(w, "Paperless URL and API token are required", http.StatusBadRequest)
		return
	}
	s.paperlessMu.Lock()
	if s.paperlessRunning {
		s.paperlessMu.Unlock()
		addLog(s.db, "info", "paperless", "Paperless import request ignored because another import is already running", nil, nil)
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	s.paperlessRunning = true
	s.paperlessMu.Unlock()
	go func() {
		defer func() {
			s.paperlessMu.Lock()
			s.paperlessRunning = false
			s.paperlessMu.Unlock()
		}()
		s.runPaperlessImport(settings)
	}()
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Server) addIMAP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	port, _ := strconv.Atoi(r.FormValue("port"))
	if port == 0 {
		port = 993
	}
	err := saveIMAPAccount(s.db, IMAPAccount{
		Name: r.FormValue("name"), Host: r.FormValue("host"), Port: port, Username: r.FormValue("username"),
		Password: r.FormValue("password"), Mailbox: defaultString(r.FormValue("mailbox"), "INBOX"),
		TLS: r.FormValue("tls") == "on", Enabled: r.FormValue("enabled") == "on",
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	addLog(s.db, "info", "imap", "Added IMAP account "+r.FormValue("name"), nil, nil)
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		u, err := authenticate(s.db, r.FormValue("username"), r.FormValue("password"))
		if err != nil {
			s.render(w, r, "login.html", map[string]any{"Error": "Invalid username or password"})
			return
		}
		token := randomToken()
		_, err = s.db.Exec(`insert into sessions(token,user_id,expires_at) values(?,?,?)`, token, u.ID, time.Now().Add(30*24*time.Hour).UTC())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "archive_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(30 * 24 * time.Hour)})
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, r, "login.html", nil)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("archive_session"); err == nil {
		_, _ = s.db.Exec(`delete from sessions where token = ?`, c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "archive_session", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			next.ServeHTTP(w, r)
			return
		}
		c, err := r.Cookie("archive_session")
		if err != nil || c.Value == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		var id int64
		err = s.db.QueryRow(`select user_id from sessions where token = ? and expires_at > ?`, c.Value, time.Now().UTC()).Scan(&id)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func safeBase(name string) string {
	name = filepath.Base(name)
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "\x00", "")
	return replacer.Replace(name)
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func removeDataFile(dataDir, rel string) {
	if rel == "" {
		return
	}
	path := filepath.Clean(filepath.Join(dataDir, rel))
	root := filepath.Clean(dataDir) + string(os.PathSeparator)
	if strings.HasPrefix(path, root) {
		_ = os.Remove(path)
	}
}
