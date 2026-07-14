package app

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"
)

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	stmts := []string{
		`create table if not exists users (
			id integer primary key autoincrement,
			username text not null unique,
			password_hash text not null
		)`,
		`create table if not exists sessions (
			token text primary key,
			user_id integer not null references users(id) on delete cascade,
			expires_at datetime not null
		)`,
		`create table if not exists documents (
			id integer primary key autoincrement,
			title text not null,
			original_name text not null,
			file_path text not null,
			preview_path text not null default '',
			sha256 text not null default '',
			source text not null default '',
			source_id text not null default '',
			tags text not null default '',
			summary text not null default '',
			entities text not null default '',
			keywords text not null default '',
			synonyms text not null default '',
			created_at datetime not null default current_timestamp,
			processed_at datetime,
			analysis_error text not null default ''
		)`,
		`create virtual table if not exists documents_fts using fts5(
			title, original_name, tags, summary, entities, keywords, synonyms,
			content='documents', content_rowid='id', tokenize='porter unicode61'
		)`,
		`create trigger if not exists documents_ai after insert on documents begin
			insert into documents_fts(rowid,title,original_name,tags,summary,entities,keywords,synonyms)
			values (new.id,new.title,new.original_name,new.tags,new.summary,new.entities,new.keywords,new.synonyms);
		end`,
		`create trigger if not exists documents_ad after delete on documents begin
			insert into documents_fts(documents_fts,rowid,title,original_name,tags,summary,entities,keywords,synonyms)
			values('delete',old.id,old.title,old.original_name,old.tags,old.summary,old.entities,old.keywords,old.synonyms);
		end`,
		`create trigger if not exists documents_au after update on documents begin
			insert into documents_fts(documents_fts,rowid,title,original_name,tags,summary,entities,keywords,synonyms)
			values('delete',old.id,old.title,old.original_name,old.tags,old.summary,old.entities,old.keywords,old.synonyms);
			insert into documents_fts(rowid,title,original_name,tags,summary,entities,keywords,synonyms)
			values (new.id,new.title,new.original_name,new.tags,new.summary,new.entities,new.keywords,new.synonyms);
		end`,
		`create table if not exists settings (
			key text primary key,
			value text not null
		)`,
		`create table if not exists imap_accounts (
			id integer primary key autoincrement,
			name text not null,
			host text not null,
			port integer not null,
			username text not null,
			password text not null,
			mailbox text not null default 'INBOX',
			tls integer not null default 1,
			enabled integer not null default 1,
			last_checked_at datetime,
			last_error text not null default '',
			created_at datetime not null default current_timestamp
		)`,
		`create table if not exists imap_processed_messages (
			account_id integer not null references imap_accounts(id) on delete cascade,
			uid integer not null,
			processed_at datetime not null default current_timestamp,
			primary key(account_id, uid)
		)`,
		`create table if not exists import_jobs (
			id integer primary key autoincrement,
			source text not null,
			status text not null,
			total integer not null default 0,
			imported integer not null default 0,
			skipped integer not null default 0,
			failed integer not null default 0,
			last_error text not null default '',
			started_at datetime not null default current_timestamp,
			finished_at datetime
		)`,
		`create table if not exists app_logs (
			id integer primary key autoincrement,
			level text not null,
			component text not null,
			message text not null,
			document_id integer,
			import_job_id integer,
			created_at datetime not null default current_timestamp
		)`,
		`create index if not exists app_logs_created_at_idx on app_logs(created_at desc)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`create unique index if not exists documents_source_unique on documents(source, source_id) where source <> '' and source_id <> ''`); err != nil {
		return err
	}
	if _, err := db.Exec(`create unique index if not exists documents_sha256_unique on documents(sha256) where sha256 <> ''`); err != nil {
		return err
	}
	return ensureDefaultUser(db)
}

func ensureDefaultUser(db *sql.DB) error {
	var count int
	if err := db.QueryRow(`select count(*) from users`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("admin"), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = db.Exec(`insert into users(username,password_hash) values(?,?)`, "admin", string(hash))
	return err
}

func authenticate(db *sql.DB, username, password string) (*User, error) {
	u := &User{}
	err := db.QueryRow(`select id, username, password_hash from users where username = ?`, username).Scan(&u.ID, &u.Username, &u.PasswordHash)
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return nil, errors.New("invalid credentials")
	}
	return u, nil
}

func getUserByID(db *sql.DB, id int64) (*User, error) {
	u := &User{}
	err := db.QueryRow(`select id, username, password_hash from users where id = ?`, id).Scan(&u.ID, &u.Username, &u.PasswordHash)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func updatePassword(db *sql.DB, userID int64, currentPassword, newPassword string) error {
	u, err := getUserByID(db, userID)
	if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(currentPassword)) != nil {
		return errors.New("current password is incorrect")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = db.Exec(`update users set password_hash = ? where id = ?`, string(hash), userID)
	return err
}

func createDocument(db *sql.DB, title, originalName, filePath, sha256 string) (int64, bool, error) {
	res, err := db.Exec(`insert or ignore into documents(title, original_name, file_path, sha256) values(?,?,?,?)`, title, originalName, filePath, sha256)
	if err != nil {
		return 0, false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, false, err
	}
	if affected == 0 {
		return 0, true, nil
	}
	id, err := res.LastInsertId()
	return id, false, err
}

func createImportedDocument(db *sql.DB, title, originalName, filePath, source, sourceID, sha256 string) (int64, bool, error) {
	res, err := db.Exec(`insert or ignore into documents(title, original_name, file_path, source, source_id, sha256) values(?,?,?,?,?,?)`, title, originalName, filePath, source, sourceID, sha256)
	if err != nil {
		return 0, false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, false, err
	}
	if affected == 0 {
		return 0, true, nil
	}
	id, err := res.LastInsertId()
	return id, false, err
}

func updateDocumentAnalysis(db *sql.DB, id int64, title string, tags []string, summary, entities, keywords, synonyms, errText string) error {
	now := time.Now().UTC()
	_, err := db.Exec(`update documents
		set title = coalesce(nullif(?, ''), title), tags = ?, summary = ?, entities = ?, keywords = ?, synonyms = ?,
			processed_at = ?, analysis_error = ?
		where id = ?`,
		title, strings.Join(tags, ","), summary, entities, keywords, synonyms, now, errText, id)
	return err
}

func updatePreview(db *sql.DB, id int64, previewPath string) error {
	_, err := db.Exec(`update documents set preview_path = ? where id = ?`, previewPath, id)
	return err
}

func updateDocumentCreatedAt(db *sql.DB, id int64, createdAt time.Time) error {
	_, err := db.Exec(`update documents set created_at = ? where id = ?`, createdAt.UTC(), id)
	return err
}

func updateDocumentMetadata(db *sql.DB, id int64, title string, tags []string) error {
	_, err := db.Exec(`update documents set title = ?, tags = ? where id = ?`, strings.TrimSpace(title), strings.Join(normalizeTags(tags), ","), id)
	return err
}

func getDocument(db *sql.DB, id int64) (*Document, error) {
	row := db.QueryRow(`select id,title,original_name,file_path,preview_path,sha256,tags,summary,entities,keywords,synonyms,created_at,processed_at,analysis_error from documents where id = ?`, id)
	return scanDocument(row)
}

func deleteDocument(db *sql.DB, id int64) error {
	_, err := db.Exec(`delete from documents where id = ?`, id)
	return err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanDocument(row scanner) (*Document, error) {
	var d Document
	var tags string
	var processed sql.NullTime
	if err := row.Scan(&d.ID, &d.Title, &d.OriginalName, &d.FilePath, &d.PreviewPath, &d.SHA256, &tags, &d.Summary, &d.Entities, &d.Keywords, &d.Synonyms, &d.CreatedAt, &processed, &d.AnalysisError); err != nil {
		return nil, err
	}
	d.Tags = splitCSV(tags)
	if processed.Valid {
		d.ProcessedAt = &processed.Time
	}
	return &d, nil
}

func listDocuments(db *sql.DB, q, tag string, limit int) ([]Document, error) {
	q = strings.TrimSpace(q)
	tag = strings.TrimSpace(tag)
	if limit <= 0 || limit > 200 {
		limit = 80
	}
	var rows *sql.Rows
	var err error
	if q != "" {
		match := ftsQuery(expandQuery(q))
		rows, err = db.Query(`select d.id,d.title,d.original_name,d.file_path,d.preview_path,d.sha256,d.tags,d.summary,d.entities,d.keywords,d.synonyms,d.created_at,d.processed_at,d.analysis_error
			from documents_fts f join documents d on d.id = f.rowid
			where documents_fts match ? and (? = '' or ',' || d.tags || ',' like '%,' || ? || ',%')
			order by bm25(documents_fts), d.created_at desc limit ?`, match, tag, tag, limit)
	} else {
		rows, err = db.Query(`select id,title,original_name,file_path,preview_path,sha256,tags,summary,entities,keywords,synonyms,created_at,processed_at,analysis_error
			from documents
			where (? = '' or ',' || tags || ',' like '%,' || ? || ',%')
			order by created_at desc limit ?`, tag, tag, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var docs []Document
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		docs = append(docs, *d)
	}
	return docs, rows.Err()
}

func allTags(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`select tags from documents where tags <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		for _, t := range splitCSV(s) {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out, rows.Err()
}

func getSettings(db *sql.DB) (Settings, error) {
	s := Settings{AIEndpoint: "https://api.openai.com/v1/chat/completions", AIModel: "gpt-4.1-mini", ScanEveryMins: 15}
	rows, err := db.Query(`select key, value from settings`)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return s, err
		}
		switch k {
		case "ai_endpoint":
			s.AIEndpoint = v
		case "ai_model":
			s.AIModel = v
		case "ai_api_key":
			s.AIAPIKey = v
		case "scan_every_mins":
			fmt.Sscanf(v, "%d", &s.ScanEveryMins)
		case "paperless_base_url":
			s.PaperlessBaseURL = v
		case "paperless_api_token":
			s.PaperlessAPIToken = v
		}
	}
	return s, rows.Err()
}

func saveSettings(db *sql.DB, s Settings) error {
	values := map[string]string{
		"ai_endpoint":         s.AIEndpoint,
		"ai_model":            s.AIModel,
		"ai_api_key":          s.AIAPIKey,
		"scan_every_mins":     fmt.Sprint(s.ScanEveryMins),
		"paperless_base_url":  s.PaperlessBaseURL,
		"paperless_api_token": s.PaperlessAPIToken,
	}
	for k, v := range values {
		if _, err := db.Exec(`insert into settings(key,value) values(?,?)
			on conflict(key) do update set value = excluded.value`, k, v); err != nil {
			return err
		}
	}
	return nil
}

func createImportJob(db *sql.DB, source string) (int64, error) {
	res, err := db.Exec(`insert into import_jobs(source,status) values(?,?)`, source, "running")
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func updateImportJob(db *sql.DB, id int64, total, imported, skipped, failed int, lastError string) {
	_, _ = db.Exec(`update import_jobs set total = ?, imported = ?, skipped = ?, failed = ?, last_error = ? where id = ?`,
		total, imported, skipped, failed, lastError, id)
}

func finishImportJob(db *sql.DB, id int64, status, lastError string) {
	_, _ = db.Exec(`update import_jobs set status = ?, last_error = ?, finished_at = ? where id = ?`, status, lastError, time.Now().UTC(), id)
}

func latestImportJob(db *sql.DB, source string) (*ImportJob, error) {
	row := db.QueryRow(`select id,source,status,total,imported,skipped,failed,last_error,started_at,finished_at
		from import_jobs where source = ? order by started_at desc limit 1`, source)
	var job ImportJob
	var finished sql.NullTime
	if err := row.Scan(&job.ID, &job.Source, &job.Status, &job.Total, &job.Imported, &job.Skipped, &job.Failed, &job.LastError, &job.StartedAt, &finished); err != nil {
		return nil, err
	}
	if finished.Valid {
		job.FinishedAt = &finished.Time
	}
	return &job, nil
}

func addLog(db *sql.DB, level, component, message string, documentID, importJobID *int64) {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" {
		level = "info"
	}
	component = strings.TrimSpace(component)
	if component == "" {
		component = "app"
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	_, _ = db.Exec(`insert into app_logs(level, component, message, document_id, import_job_id) values(?,?,?,?,?)`,
		level, component, message, nullableInt64(documentID), nullableInt64(importJobID))
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func listLogs(db *sql.DB, from, to *time.Time, limit int) ([]AppLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query := `select id,level,component,message,document_id,import_job_id,created_at from app_logs where 1=1`
	var args []any
	if from != nil {
		query += ` and created_at >= ?`
		args = append(args, from.UTC())
	}
	if to != nil {
		query += ` and created_at <= ?`
		args = append(args, to.UTC())
	}
	query += ` order by created_at desc limit ?`
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var logs []AppLog
	for rows.Next() {
		var entry AppLog
		var documentID, importJobID sql.NullInt64
		if err := rows.Scan(&entry.ID, &entry.Level, &entry.Component, &entry.Message, &documentID, &importJobID, &entry.CreatedAt); err != nil {
			return nil, err
		}
		if documentID.Valid {
			entry.DocumentID = &documentID.Int64
		}
		if importJobID.Valid {
			entry.ImportJobID = &importJobID.Int64
		}
		logs = append(logs, entry)
	}
	return logs, rows.Err()
}

func listIMAPAccounts(db *sql.DB) ([]IMAPAccount, error) {
	rows, err := db.Query(`select id,name,host,port,username,password,mailbox,tls,enabled,last_checked_at,last_error,created_at from imap_accounts order by created_at desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []IMAPAccount
	for rows.Next() {
		var a IMAPAccount
		var tls, enabled int
		var checked sql.NullTime
		if err := rows.Scan(&a.ID, &a.Name, &a.Host, &a.Port, &a.Username, &a.Password, &a.Mailbox, &tls, &enabled, &checked, &a.LastError, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.TLS = tls == 1
		a.Enabled = enabled == 1
		if checked.Valid {
			a.LastCheckedAt = &checked.Time
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

func saveIMAPAccount(db *sql.DB, a IMAPAccount) error {
	_, err := db.Exec(`insert into imap_accounts(name,host,port,username,password,mailbox,tls,enabled)
		values(?,?,?,?,?,?,?,?)`, a.Name, a.Host, a.Port, a.Username, a.Password, a.Mailbox, boolInt(a.TLS), boolInt(a.Enabled))
	return err
}

func getIMAPAccount(db *sql.DB, id int64) (*IMAPAccount, error) {
	row := db.QueryRow(`select id,name,host,port,username,password,mailbox,tls,enabled,last_checked_at,last_error,created_at from imap_accounts where id = ?`, id)
	var a IMAPAccount
	var tls, enabled int
	var checked sql.NullTime
	if err := row.Scan(&a.ID, &a.Name, &a.Host, &a.Port, &a.Username, &a.Password, &a.Mailbox, &tls, &enabled, &checked, &a.LastError, &a.CreatedAt); err != nil {
		return nil, err
	}
	a.TLS = tls == 1
	a.Enabled = enabled == 1
	if checked.Valid {
		a.LastCheckedAt = &checked.Time
	}
	return &a, nil
}

func deleteIMAPAccount(db *sql.DB, id int64) error {
	_, err := db.Exec(`delete from imap_accounts where id = ?`, id)
	return err
}

func enabledIMAPAccounts(db *sql.DB) ([]IMAPAccount, error) {
	rows, err := db.Query(`select id,name,host,port,username,password,mailbox,tls,enabled,last_checked_at,last_error,created_at from imap_accounts where enabled = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []IMAPAccount
	for rows.Next() {
		var a IMAPAccount
		var tls, enabled int
		var checked sql.NullTime
		if err := rows.Scan(&a.ID, &a.Name, &a.Host, &a.Port, &a.Username, &a.Password, &a.Mailbox, &tls, &enabled, &checked, &a.LastError, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.TLS = tls == 1
		a.Enabled = enabled == 1
		if checked.Valid {
			a.LastCheckedAt = &checked.Time
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

func markIMAPChecked(db *sql.DB, id int64, errText string) {
	_, _ = db.Exec(`update imap_accounts set last_checked_at = ?, last_error = ? where id = ?`, time.Now().UTC(), errText, id)
}

func processedIMAPUIDs(db *sql.DB, accountID int64, uids []uint32) (map[uint32]bool, error) {
	seen := map[uint32]bool{}
	if len(uids) == 0 {
		return seen, nil
	}
	for _, uid := range uids {
		var exists int
		err := db.QueryRow(`select 1 from imap_processed_messages where account_id = ? and uid = ?`, accountID, uid).Scan(&exists)
		if err == nil {
			seen[uid] = true
			continue
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
	}
	return seen, nil
}

func markIMAPUIDProcessed(db *sql.DB, accountID int64, uid uint32) {
	_, _ = db.Exec(`insert or ignore into imap_processed_messages(account_id, uid) values(?,?)`, accountID, uid)
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
