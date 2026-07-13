package app

import "time"

type Config struct {
	Addr    string
	DataDir string
}

type User struct {
	ID           int64
	Username     string
	PasswordHash string
}

type Document struct {
	ID            int64
	Title         string
	OriginalName  string
	FilePath      string
	PreviewPath   string
	SHA256        string
	Tags          []string
	Summary       string
	Entities      string
	Keywords      string
	Synonyms      string
	CreatedAt     time.Time
	ProcessedAt   *time.Time
	AnalysisError string
}

type Settings struct {
	AIEndpoint        string
	AIModel           string
	AIAPIKey          string
	ScanEveryMins     int
	PaperlessBaseURL  string
	PaperlessAPIToken string
}

type IMAPAccount struct {
	ID            int64
	Name          string
	Host          string
	Port          int
	Username      string
	Password      string
	Mailbox       string
	TLS           bool
	Enabled       bool
	LastCheckedAt *time.Time
	LastError     string
	CreatedAt     time.Time
}

type ImportJob struct {
	ID         int64
	Source     string
	Status     string
	Total      int
	Imported   int
	Skipped    int
	Failed     int
	LastError  string
	StartedAt  time.Time
	FinishedAt *time.Time
}

type AppLog struct {
	ID          int64
	Level       string
	Component   string
	Message     string
	DocumentID  *int64
	ImportJobID *int64
	CreatedAt   time.Time
}
