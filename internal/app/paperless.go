package app

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type paperlessListResponse struct {
	Count   int                 `json:"count"`
	Next    string              `json:"next"`
	Results []paperlessDocument `json:"results"`
}

type paperlessDocument struct {
	ID               int    `json:"id"`
	Title            string `json:"title"`
	OriginalFileName string `json:"original_file_name"`
	ArchiveFileName  string `json:"archive_filename"`
}

type paperlessImportStats struct {
	Total     int
	Imported  int
	Skipped   int
	Failed    int
	LastError string
}

func (s *Server) runPaperlessImport(settings Settings) {
	jobID, err := createImportJob(s.db, "paperless-ngx")
	if err != nil {
		addLog(s.db, "error", "paperless", "Could not create Paperless import job: "+err.Error(), nil, nil)
		return
	}
	addLog(s.db, "info", "paperless", "Started Paperless-ngx import", nil, &jobID)
	stats := paperlessImportStats{}
	client := &http.Client{Timeout: 90 * time.Second}
	baseURL := strings.TrimRight(settings.PaperlessBaseURL, "/")
	pageURL := baseURL + "/api/documents/?page_size=100"
	for pageURL != "" {
		list, err := fetchPaperlessPage(client, pageURL, settings.PaperlessAPIToken)
		if err != nil {
			stats.LastError = err.Error()
			updateImportJob(s.db, jobID, stats.Total, stats.Imported, stats.Skipped, stats.Failed, stats.LastError)
			finishImportJob(s.db, jobID, "failed", stats.LastError)
			addLog(s.db, "error", "paperless", "Paperless-ngx import failed: "+stats.LastError, nil, &jobID)
			return
		}
		stats.Total = list.Count
		for _, doc := range list.Results {
			if err := s.importPaperlessDocument(client, baseURL, settings.PaperlessAPIToken, doc); err != nil {
				if err == errPaperlessDuplicate {
					stats.Skipped++
				} else {
					stats.Failed++
					stats.LastError = err.Error()
					addLog(s.db, "error", "paperless", fmt.Sprintf("Paperless document %d failed: %v", doc.ID, err), nil, &jobID)
				}
			} else {
				stats.Imported++
			}
			updateImportJob(s.db, jobID, stats.Total, stats.Imported, stats.Skipped, stats.Failed, stats.LastError)
		}
		pageURL = list.Next
	}
	status := "completed"
	if stats.Failed > 0 {
		status = "completed_with_errors"
	}
	finishImportJob(s.db, jobID, status, stats.LastError)
	addLog(s.db, "info", "paperless", fmt.Sprintf("Paperless-ngx import finished: imported=%d skipped=%d failed=%d", stats.Imported, stats.Skipped, stats.Failed), nil, &jobID)
}

func fetchPaperlessPage(client *http.Client, url, token string) (paperlessListResponse, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return paperlessListResponse{}, err
	}
	req.Header.Set("Authorization", "Token "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return paperlessListResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return paperlessListResponse{}, fmt.Errorf("Paperless list request failed: %s", resp.Status)
	}
	var list paperlessListResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return paperlessListResponse{}, err
	}
	return list, nil
}

var errPaperlessDuplicate = fmt.Errorf("document already imported")

func (s *Server) importPaperlessDocument(client *http.Client, baseURL, token string, doc paperlessDocument) error {
	sourceID := strconv.Itoa(doc.ID)
	name := paperlessFileName(doc)
	title := doc.Title
	if strings.TrimSpace(title) == "" {
		title = strings.TrimSuffix(name, filepath.Ext(name))
	}
	tmp, err := os.CreateTemp(s.cfg.DataDir, "paperless-*.pdf")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := downloadPaperlessPDF(client, baseURL, token, doc.ID, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	hash, err := fileSHA256(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	id, duplicate, err := createImportedDocument(s.db, title, name, "", "paperless-ngx", sourceID, hash)
	if err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if duplicate {
		_ = os.Remove(tmpPath)
		return errPaperlessDuplicate
	}
	rel := filepath.Join("pdfs", fmt.Sprintf("%d-paperless-%s", id, safeBase(name)))
	dstPath := filepath.Join(s.cfg.DataDir, rel)
	if err := os.Rename(tmpPath, dstPath); err != nil {
		_ = deleteDocument(s.db, id)
		_ = os.Remove(tmpPath)
		return err
	}
	if _, err := s.db.Exec(`update documents set file_path = ? where id = ?`, rel, id); err != nil {
		return err
	}
	addLog(s.db, "info", "paperless", fmt.Sprintf("Imported Paperless document %d as %s", doc.ID, name), &id, nil)
	go s.processDocument(id)
	return nil
}

func paperlessFileName(doc paperlessDocument) string {
	for _, candidate := range []string{doc.OriginalFileName, doc.ArchiveFileName, doc.Title} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			if !strings.HasSuffix(strings.ToLower(candidate), ".pdf") {
				candidate += ".pdf"
			}
			return safeBase(candidate)
		}
	}
	return fmt.Sprintf("paperless-%d.pdf", doc.ID)
}

func downloadPaperlessPDF(client *http.Client, baseURL, token string, id int, dstPath string) error {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/documents/%d/download/", baseURL, id), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Token "+token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("Paperless download %d failed: %s", id, resp.Status)
	}
	contentType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if contentType != "" && contentType != "application/pdf" && contentType != "application/octet-stream" {
		return fmt.Errorf("Paperless download %d returned %s, not a PDF", id, contentType)
	}
	out, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
