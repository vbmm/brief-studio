package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"briefstudio/viewer"
	"briefstudio/web/internal/engine"
)

const maxDocument = 512 << 10
const readerMarker = "__EMBEDDED_BRIEF_BASE64__"

var briefID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var hostUUID = regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type library struct {
	dir    string
	hostID string
}

type briefEntry struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	GeneratedAt string `json:"generatedAt"`
}

func openLibrary(dataDir string) (*library, error) {
	dir := filepath.Join(dataDir, "briefs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	id, err := openHostID(filepath.Join(dataDir, "host-id"))
	if err != nil {
		return nil, err
	}
	return &library{dir: dir, hostID: id}, nil
}

func openHostID(path string) (string, error) {
	body, err := readLimited(path, 128)
	if err == nil {
		id := strings.TrimSpace(string(body))
		if hostUUID.MatchString(id) {
			return id, nil
		}
		return "", errors.New("identifiant hôte local invalide")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	id := fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", bytes[:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:])
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return openHostID(path)
	}
	if err != nil {
		return "", err
	}
	if _, err = io.WriteString(file, id+"\n"); err != nil {
		_ = file.Close()
		return "", err
	}
	return id, file.Close()
}

func readLimited(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, errors.New("fichier local invalide")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if int64(len(data)) > maxBytes {
		return nil, errors.New("fichier local trop volumineux")
	}
	return data, err
}

func (l library) get(id string) (engine.Document, error) {
	var doc engine.Document
	if !briefID.MatchString(id) {
		return doc, fs.ErrNotExist
	}
	body, err := readLimited(filepath.Join(l.dir, id+".json"), maxDocument)
	if err != nil {
		return doc, err
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return doc, err
	}
	if !validDocument(doc) {
		return doc, errors.New("format de brief local invalide")
	}
	return doc, nil
}

func validDocument(doc engine.Document) bool {
	_, err := time.Parse(time.RFC3339Nano, doc.GeneratedAt)
	return err == nil && doc.Schema == "nq-es-brief/v1" && len(doc.Title) <= 240 &&
		strings.TrimSpace(doc.Title) != "" && strings.TrimSpace(doc.Text) != ""
}

func (l library) save(doc engine.Document) (string, error) {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil || !validDocument(doc) || len(data) > maxDocument {
		return "", errors.New("brief non enregistrable")
	}
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(bytes[:])
	f, err := os.CreateTemp(l.dir, ".pending-*.json")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return "", err
	}
	if err := os.Rename(f.Name(), filepath.Join(l.dir, id+".json")); err != nil {
		return "", err
	}
	return id, nil
}

func (l library) list() ([]briefEntry, error) {
	files, err := os.ReadDir(l.dir)
	if err != nil {
		return nil, err
	}
	entries := make([]briefEntry, 0)
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") || !file.Type().IsRegular() {
			continue
		}
		id := strings.TrimSuffix(file.Name(), ".json")
		doc, err := l.get(id)
		if err != nil {
			continue
		}
		entries = append(entries, briefEntry{id, doc.Title, doc.GeneratedAt})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].GeneratedAt > entries[j].GeneratedAt })
	return entries, nil
}

func (s *studioServer) readBrief(w http.ResponseWriter, r *http.Request) {
	doc, err := s.library.get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Brief introuvable.")
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *studioServer) exportBrief(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	doc := engine.Sample()
	filename := "Exemple-brief-NQ-ES.html"
	if id != "sample" {
		loaded, err := s.library.get(id)
		if err != nil {
			writeError(w, http.StatusNotFound, "Brief introuvable.")
			return
		}
		doc = loaded
		day, _ := time.Parse(time.RFC3339Nano, doc.GeneratedAt)
		filename = "Brief-NQ-ES-" + day.Format("2006-01-02") + ".html"
	}
	doc.LocalGEX = ""
	body, err := json.Marshal(doc)
	if err != nil || strings.Count(viewer.Template, readerMarker) != 1 || len(body) > maxDocument {
		writeError(w, http.StatusInternalServerError, "Le fichier HTML n’a pas pu être créé.")
		return
	}
	html := strings.Replace(viewer.Template, readerMarker, base64.StdEncoding.EncodeToString(body), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	_, _ = io.WriteString(w, html)
}
