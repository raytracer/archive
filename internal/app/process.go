package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type aiResult struct {
	Title    string   `json:"title"`
	Tags     []string `json:"tags"`
	Summary  string   `json:"summary"`
	Entities flexList `json:"entities"`
	Keywords flexList `json:"keywords"`
	Synonyms flexList `json:"synonyms"`
}

type flexList []string

func (f *flexList) UnmarshalJSON(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*f = flattenJSONStrings(value)
	return nil
}

func flattenJSONStrings(value any) []string {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	case []any:
		var out []string
		for _, item := range v {
			out = append(out, flattenJSONStrings(item)...)
		}
		return out
	case map[string]any:
		var out []string
		for key, item := range v {
			nested := flattenJSONStrings(item)
			if len(nested) == 0 {
				out = append(out, key)
				continue
			}
			for _, entry := range nested {
				out = append(out, key+": "+entry)
			}
		}
		return out
	case float64:
		return []string{fmt.Sprint(v)}
	case bool:
		return []string{fmt.Sprint(v)}
	default:
		return []string{fmt.Sprint(v)}
	}
}

func (s *Server) processDocument(id int64) {
	doc, err := getDocument(s.db, id)
	if err != nil {
		addLog(s.db, "error", "document", fmt.Sprintf("Document %d could not be loaded for processing: %v", id, err), &id, nil)
		return
	}
	addLog(s.db, "info", "document", "Started processing "+doc.OriginalName, &id, nil)
	absPDF := filepath.Join(s.cfg.DataDir, doc.FilePath)
	previewRel := doc.PreviewPath
	if preview, err := s.generatePreview(id, absPDF); err == nil && preview != "" {
		_ = updatePreview(s.db, id, preview)
		previewRel = preview
		addLog(s.db, "info", "preview", "Generated preview for "+doc.OriginalName, &id, nil)
	} else if err != nil {
		addLog(s.db, "error", "preview", fmt.Sprintf("Preview generation failed for %s: %v", doc.OriginalName, err), &id, nil)
	}
	text := extractPDFText(absPDF)
	imageDataURL := ""
	if strings.TrimSpace(text) == "" && previewRel != "" {
		imageDataURL = imageDataURLFromFile(filepath.Join(s.cfg.DataDir, previewRel))
	}
	result, err := s.analyzeWithAI(doc, text, imageDataURL)
	if err != nil {
		heur := heuristicAnalysis(doc, text)
		_ = updateDocumentAnalysis(s.db, id, heur.Title, heur.Tags, heur.Summary, strings.Join([]string(heur.Entities), ", "), strings.Join([]string(heur.Keywords), ", "), strings.Join([]string(heur.Synonyms), ", "), err.Error())
		addLog(s.db, "error", "ai", fmt.Sprintf("AI analysis failed for %s: %v", doc.OriginalName, err), &id, nil)
		return
	}
	_ = updateDocumentAnalysis(s.db, id, result.Title, normalizeTags(result.Tags), result.Summary, strings.Join([]string(result.Entities), ", "), strings.Join([]string(result.Keywords), ", "), strings.Join([]string(result.Synonyms), ", "), "")
	addLog(s.db, "info", "ai", "AI analysis completed for "+doc.OriginalName, &id, nil)
}

func (s *Server) generatePreview(id int64, pdfPath string) (string, error) {
	outPrefix := filepath.Join(s.cfg.DataDir, "previews", fmt.Sprintf("%d", id))
	cmd := exec.Command("pdftoppm", "-f", "1", "-singlefile", "-png", "-scale-to", "900", pdfPath, outPrefix)
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return filepath.Join("previews", fmt.Sprintf("%d.png", id)), nil
}

func extractPDFText(pdfPath string) string {
	cmd := exec.Command("pdftotext", "-layout", "-enc", "UTF-8", pdfPath, "-")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(out))
	if len(text) > 60000 {
		text = text[:60000]
	}
	return text
}

func heuristicAnalysis(doc *Document, text string) aiResult {
	title := doc.Title
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) > 8 && len(line) < 120 {
			title = line
			break
		}
	}
	tokens := expandQuery(title + " " + text)
	tags := []string{}
	for _, token := range tokens {
		if len(token) > 4 && len(tags) < 8 {
			tags = append(tags, token)
		}
	}
	summary := strings.Join(strings.Fields(text), " ")
	if len(summary) > 700 {
		summary = summary[:700]
	}
	return aiResult{Title: title, Tags: normalizeTags(tags), Summary: summary, Keywords: flexList(tokens), Synonyms: flexList(relatedSynonyms(tokens))}
}

func relatedSynonyms(tokens []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, token := range tokens {
		for _, syn := range synonymSeeds[token] {
			if !seen[syn] {
				seen[syn] = true
				out = append(out, syn)
			}
		}
	}
	return out
}

func imageDataURLFromFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return ""
	}
	ext := strings.ToLower(filepath.Ext(path))
	mediaType := "image/png"
	if ext == ".jpg" || ext == ".jpeg" {
		mediaType = "image/jpeg"
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func (s *Server) analyzeWithAI(doc *Document, text, imageDataURL string) (aiResult, error) {
	settings, err := getSettings(s.db)
	if err != nil {
		return aiResult{}, err
	}
	if settings.AIAPIKey == "" {
		return aiResult{}, fmt.Errorf("AI API key is not configured")
	}
	prompt := `Analyze this PDF for a personal document archive. Return strict JSON with:
title: concise human-readable German title,
tags: 3-10 normalized lowercase tags,
summary: dense useful German summary,
entities: important people, companies, accounts, dates, amounts, locations,
keywords: search terms users may type,
synonyms: equivalent or adjacent search terms that should find this document.
If extracted text is empty and an image is provided, analyze the visible document image carefully with OCR-like attention.
The title and summary must always be written in German, regardless of the document language.
Do not include markdown.`
	userText := "Filename: " + doc.OriginalName
	if strings.TrimSpace(text) != "" {
		userText += "\n\nExtracted text:\n" + text
	} else {
		userText += "\n\nNo extracted text was available. Analyze the attached first-page image if present."
	}
	userContent := any(userText)
	if imageDataURL != "" {
		userContent = []map[string]any{
			{"type": "text", "text": userText},
			{"type": "image_url", "image_url": map[string]any{"url": imageDataURL}},
		}
	}
	body := map[string]any{
		"model": settings.AIModel,
		"messages": []map[string]any{
			{"role": "system", "content": prompt},
			{"role": "user", "content": userContent},
		},
		"temperature": 0.1,
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, settings.AIEndpoint, bytes.NewReader(raw))
	if err != nil {
		return aiResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+settings.AIAPIKey)
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return aiResult{}, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode >= 300 {
		return aiResult{}, fmt.Errorf("AI request failed: %s", resp.Status)
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return aiResult{}, err
	}
	if len(envelope.Choices) == 0 {
		return aiResult{}, fmt.Errorf("AI returned no choices")
	}
	content := strings.TrimSpace(envelope.Choices[0].Message.Content)
	content = strings.TrimPrefix(strings.TrimSuffix(content, "```"), "```json")
	var result aiResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &result); err != nil {
		return aiResult{}, err
	}
	result.Tags = normalizeTags(result.Tags)
	return result, nil
}

func isPDFPart(filename, contentType string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == ".pdf" {
		return true
	}
	mt, _, _ := mime.ParseMediaType(contentType)
	return mt == "application/pdf"
}
