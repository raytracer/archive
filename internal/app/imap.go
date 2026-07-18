package app

import (
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset"
)

func (s *Server) imapLoop() {
	var lastScan time.Time
	for {
		settings, _ := getSettings(s.db)
		mins := settings.ScanEveryMins
		if mins < 1 {
			mins = 15
		}
		interval := time.Duration(mins) * time.Minute
		if lastScan.IsZero() || time.Since(lastScan) >= interval {
			s.scanIMAPAccounts()
			lastScan = time.Now()
		}
		time.Sleep(time.Minute)
	}
}

func (s *Server) scanIMAPAccounts() {
	accounts, err := enabledIMAPAccounts(s.db)
	if err != nil {
		addLog(s.db, "error", "imap", "Could not list enabled IMAP accounts: "+err.Error(), nil, nil)
		return
	}
	addLog(s.db, "info", "imap", fmt.Sprintf("Starting IMAP scan for %d account(s)", len(accounts)), nil, nil)
	for _, account := range accounts {
		if err := s.scanIMAPAccount(account); err != nil {
			markIMAPChecked(s.db, account.ID, err.Error())
			addLog(s.db, "error", "imap", fmt.Sprintf("IMAP scan failed for %s: %v", account.Name, err), nil, nil)
		} else {
			markIMAPChecked(s.db, account.ID, "")
			addLog(s.db, "info", "imap", "IMAP scan completed for "+account.Name, nil, nil)
		}
	}
}

func (s *Server) scanIMAPAccount(a IMAPAccount) error {
	addr := fmt.Sprintf("%s:%d", a.Host, a.Port)
	var c *client.Client
	var err error
	if a.TLS {
		c, err = client.DialTLS(addr, &tls.Config{ServerName: a.Host})
	} else {
		c, err = client.Dial(addr)
	}
	if err != nil {
		return err
	}
	defer c.Logout()
	if err := c.Login(a.Username, a.Password); err != nil {
		return err
	}
	mbox, err := c.Select(a.Mailbox, true)
	if err != nil {
		return err
	}
	if mbox.Messages == 0 {
		return nil
	}
	search := &imap.SearchCriteria{}
	uids, err := c.UidSearch(search)
	if err != nil {
		return err
	}
	seen, err := processedIMAPUIDs(s.db, a.ID, uids)
	if err != nil {
		return err
	}
	var pending []uint32
	for _, uid := range uids {
		if !seen[uid] {
			pending = append(pending, uid)
		}
	}
	const maxMessagesPerScan = 100
	if len(pending) > maxMessagesPerScan {
		pending = pending[len(pending)-maxMessagesPerScan:]
	}
	addLog(s.db, "info", "imap", fmt.Sprintf("IMAP account %s has %d unprocessed message(s); scanning %d", a.Name, len(uids)-len(seen), len(pending)), nil, nil)
	if len(pending) == 0 {
		return nil
	}
	seqset := new(imap.SeqSet)
	seqset.AddNum(pending...)
	section := &imap.BodySectionName{}
	items := []imap.FetchItem{imap.FetchUid, section.FetchItem()}
	messages := make(chan *imap.Message, 10)
	done := make(chan error, 1)
	go func() { done <- c.UidFetch(seqset, items, messages) }()
	for msg := range messages {
		if msg.Uid == 0 {
			continue
		}
		r := msg.GetBody(section)
		if r == nil {
			markIMAPUIDProcessed(s.db, a.ID, msg.Uid)
			continue
		}
		if err := s.importPDFAttachments(r); err != nil {
			log.Printf("imap attachment import failed: %v", err)
			addLog(s.db, "error", "imap", "IMAP attachment import failed: "+err.Error(), nil, nil)
		}
		markIMAPUIDProcessed(s.db, a.ID, msg.Uid)
	}
	return <-done
}

func (s *Server) importPDFAttachments(r io.Reader) error {
	entity, err := message.Read(r)
	if err != nil {
		return err
	}
	var messageDate *time.Time
	if parsed, err := mail.ParseDate(entity.Header.Get("Date")); err == nil {
		messageDate = &parsed
	}
	return walkEntity(entity, func(filename, contentType string, body io.Reader) error {
		if !isPDFPart(filename, contentType) {
			return nil
		}
		if filename == "" {
			filename = "email-attachment.pdf"
		}
		tmp, err := os.CreateTemp("", "archive-mail-*.pdf")
		if err != nil {
			return err
		}
		if _, err := io.Copy(tmp, body); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return err
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmp.Name())
			return err
		}
		defer os.Remove(tmp.Name())
		return s.importPDFFile(tmp.Name(), filename, messageDate)
	})
}

func walkEntity(e *message.Entity, fn func(filename, contentType string, body io.Reader) error) error {
	contentType, params, _ := e.Header.ContentType()
	if strings.HasPrefix(contentType, "multipart/") {
		mr := e.MultipartReader()
		if mr == nil {
			return nil
		}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if err := walkEntity(part, fn); err != nil {
				return err
			}
		}
	}
	filename := params["name"]
	if disp, dispParams, err := mime.ParseMediaType(e.Header.Get("Content-Disposition")); err == nil {
		if disp == "attachment" && dispParams["filename"] != "" {
			filename = dispParams["filename"]
		}
	}
	filename = decodeMIMEFilename(filename)
	return fn(filename, contentType, e.Body)
}

func decodeMIMEFilename(filename string) string {
	decoded, err := new(mime.WordDecoder).DecodeHeader(filename)
	if err != nil {
		return filename
	}
	return decoded
}

func (s *Server) importPDFFile(path, originalName string, createdAt *time.Time) error {
	name := safeBase(originalName)
	hash, err := fileSHA256(path)
	if err != nil {
		return err
	}
	id, duplicate, err := createDocument(s.db, strings.TrimSuffix(name, filepath.Ext(name)), name, "", hash)
	if err != nil {
		return err
	}
	if duplicate {
		addLog(s.db, "info", "imap", "Skipped duplicate PDF attachment "+name, nil, nil)
		return nil
	}
	if createdAt != nil {
		_ = updateDocumentCreatedAt(s.db, id, *createdAt)
	}
	rel := filepath.Join("pdfs", fmt.Sprintf("%d-%s", id, name))
	dstPath := filepath.Join(s.cfg.DataDir, rel)
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	_, err = s.db.Exec(`update documents set file_path = ? where id = ?`, rel, id)
	if err != nil {
		return err
	}
	addLog(s.db, "info", "imap", "Imported PDF attachment "+name, &id, nil)
	go s.processDocument(id)
	return nil
}

var _ = multipart.ErrMessageTooLarge
