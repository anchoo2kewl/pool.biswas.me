package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif" // registered so GIF uploads decode
	"image/jpeg"
	_ "image/png" // registered so PNG uploads decode
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // registered so WebP uploads decode

	"github.com/biswas-dev/pool/internal/store"
)

const (
	// maxUploadBytes is the hard limit on what will be accepted at all.
	maxUploadBytes = 25 << 20 // 25 MB
	// maxImageDimension is the longest edge kept after downscaling. A receipt
	// or a test sheet stays readable well below this.
	maxImageDimension = 1600
	// jpegQuality trades a little fidelity for a large size reduction.
	jpegQuality = 72
)

// attachmentsDir is where uploaded files live, under the data directory.
func (s *Server) attachmentsDir() string { return filepath.Join(s.Cfg.DataDir, "attachments") }

func (s *Server) handleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+(1<<20))

	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("upload too large; the limit is %d MB", maxUploadBytes>>20))
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "a file field is required")
		return
	}
	defer file.Close()

	if header.Size > maxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("file is %.1f MB; the limit is %d MB", float64(header.Size)/(1<<20), maxUploadBytes>>20))
		return
	}

	poolID := formInt(r, "pool_id")
	pool, err := s.ownedPool(r, poolID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	raw, err := io.ReadAll(io.LimitReader(file, maxUploadBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the upload")
		return
	}
	originalSize := int64(len(raw))

	// Images are downscaled and re-encoded as JPEG; anything else (a PDF, say)
	// is stored as uploaded.
	data, contentType, width, height := compressImage(raw, header.Filename)

	if err := os.MkdirAll(s.attachmentsDir(), 0o755); err != nil {
		log.Printf("create attachments dir: %v", err)
		writeError(w, http.StatusInternalServerError, "could not store the file")
		return
	}
	sum := sha256.Sum256(data)
	storedName := hex.EncodeToString(sum[:]) + extensionFor(contentType, header.Filename)
	path := filepath.Join(s.attachmentsDir(), storedName)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Printf("write attachment: %v", err)
		writeError(w, http.StatusInternalServerError, "could not store the file")
		return
	}

	kind := strings.ToLower(strings.TrimSpace(r.FormValue("kind")))
	if kind != "test_sheet" {
		kind = "receipt"
	}
	rec := &store.Receipt{
		PoolID: pool.ID, UserID: &u.ID, Filename: header.Filename, StoredName: storedName,
		ContentType: contentType, SizeBytes: int64(len(data)), OriginalBytes: originalSize,
		Width: int64(width), Height: int64(height), Kind: kind,
		Vendor: r.FormValue("vendor"), Notes: r.FormValue("notes"),
		Currency: orDefault(r.FormValue("currency"), "CAD"),
	}
	if v := r.FormValue("total"); v != "" {
		rec.TotalCents = int64(math.Round(parseFloat(v) * 100))
	}
	if v := formInt(r, "total_cents"); v > 0 {
		rec.TotalCents = v
	}
	if v := r.FormValue("purchased_on"); v != "" {
		if d, err := normaliseDate(v); err == nil {
			rec.PurchasedOn = d
		}
	}
	if v := formInt(r, "test_id"); v > 0 {
		if _, err := s.DB.Test(u.ID, v); err == nil {
			rec.TestID = &v
		}
	}

	created, err := s.DB.CreateReceipt(rec)
	if err != nil {
		os.Remove(path)
		writeStoreError(w, err)
		return
	}
	if entryID := formInt(r, "log_entry_id"); entryID > 0 {
		if _, err := s.DB.LogEntryOwner(u.ID, entryID); err == nil {
			s.DB.LinkReceipt(created.ID, entryID)
		}
	}
	writeJSON(w, http.StatusCreated, created)
}

// compressImage downscales and re-encodes an image so stored attachments stay
// small. Non-images are returned untouched.
func compressImage(raw []byte, filename string) (data []byte, contentType string, width, height int) {
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		// Not a decodable image (PDF, HEIC without a decoder, corrupt file):
		// keep it as-is so the user does not lose the document.
		return raw, detectContentType(raw, filename), 0, 0
	}

	b := img.Bounds()
	origW, origH := b.Dx(), b.Dy()
	w, h := origW, origH
	if w > maxImageDimension || h > maxImageDimension {
		scale := float64(maxImageDimension) / float64(max(w, h))
		nw, nh := int(float64(w)*scale), int(float64(h)*scale)
		dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
		// CatmullRom keeps small print on a receipt legible after downscaling.
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
		img, w, h = dst, nw, nh
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return raw, detectContentType(raw, filename), origW, origH
	}
	// If re-encoding produced something larger (an already-optimised image, or
	// synthetic noise that JPEG cannot compress), keep the original bytes —
	// and report the original dimensions with them, since that is what is
	// actually being stored.
	if buf.Len() >= len(raw) {
		return raw, detectContentType(raw, filename), origW, origH
	}
	return buf.Bytes(), "image/jpeg", w, h
}

func detectContentType(raw []byte, filename string) string {
	if ct := http.DetectContentType(raw); ct != "application/octet-stream" {
		return ct
	}
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".pdf":
		return "application/pdf"
	case ".heic", ".heif":
		return "image/heic"
	default:
		return "application/octet-stream"
	}
}

func extensionFor(contentType, filename string) string {
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	}
	if ext := filepath.Ext(filename); ext != "" && len(ext) <= 6 {
		return strings.ToLower(ext)
	}
	return ".bin"
}

func (s *Server) handleListAttachments(w http.ResponseWriter, r *http.Request) {
	f := store.ReceiptFilter{
		PoolID: queryInt(r, "pool_id"),
		Kind:   r.URL.Query().Get("kind"),
		TestID: queryInt(r, "test_id"),
	}
	list, err := s.DB.ListReceipts(userFrom(r).ID, f)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleServeAttachment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid attachment id")
		return
	}
	rec, err := s.DB.Receipt(userFrom(r).ID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Join through Base to keep a crafted stored_name from escaping the dir.
	path := filepath.Join(s.attachmentsDir(), filepath.Base(rec.StoredName))
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "the file is no longer on disk")
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the file")
		return
	}
	w.Header().Set("Content-Type", rec.ContentType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+sanitiseFilename(rec.Filename)+`"`)
	}
	http.ServeContent(w, r, rec.Filename, stat.ModTime(), f)
}

func (s *Server) handleUpdateAttachment(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid attachment id")
		return
	}
	rec, err := s.DB.Receipt(u.ID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req struct {
		Total       *float64 `json:"total"`
		TotalCents  *int64   `json:"total_cents"`
		Currency    string   `json:"currency"`
		Vendor      string   `json:"vendor"`
		PurchasedOn string   `json:"purchased_on"`
		Notes       string   `json:"notes"`
		Kind        string   `json:"kind"`
		TestID      *int64   `json:"test_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.TotalCents != nil {
		rec.TotalCents = *req.TotalCents
	} else if req.Total != nil {
		rec.TotalCents = int64(math.Round(*req.Total * 100))
	}
	if req.Currency != "" {
		rec.Currency = req.Currency
	}
	if req.Kind == "test_sheet" || req.Kind == "receipt" {
		rec.Kind = req.Kind
	}
	if req.PurchasedOn != "" {
		if d, err := normaliseDate(req.PurchasedOn); err == nil {
			rec.PurchasedOn = d
		}
	}
	if req.TestID != nil {
		rec.TestID = req.TestID
	}
	rec.Vendor, rec.Notes = req.Vendor, req.Notes
	if err := s.DB.UpdateReceipt(rec); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleLinkAttachment(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid attachment id")
		return
	}
	if _, err := s.DB.Receipt(u.ID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	var req struct {
		LogEntryID int64 `json:"log_entry_id"`
		Unlink     bool  `json:"unlink"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.DB.LogEntryOwner(u.ID, req.LogEntryID); err != nil {
		writeError(w, http.StatusBadRequest, "unknown log_entry_id")
		return
	}
	if req.Unlink {
		err = s.DB.UnlinkReceipt(id, req.LogEntryID)
	} else {
		err = s.DB.LinkReceipt(id, req.LogEntryID)
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDeleteAttachment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid attachment id")
		return
	}
	storedName, err := s.DB.DeleteReceipt(userFrom(r).ID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// The same bytes may back another row (content-addressed names), so only
	// remove the file when nothing else references it.
	var others int64
	s.DB.QueryRow(`SELECT COUNT(*) FROM receipts WHERE stored_name = ?`, storedName).Scan(&others)
	if others == 0 {
		os.Remove(filepath.Join(s.attachmentsDir(), filepath.Base(storedName)))
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func sanitiseFilename(name string) string {
	name = filepath.Base(name)
	return strings.NewReplacer(`"`, "", "\\", "", "\r", "", "\n", "").Replace(name)
}

func formInt(r *http.Request, name string) int64 {
	var n int64
	fmt.Sscanf(r.FormValue(name), "%d", &n)
	return n
}

func parseFloat(s string) float64 {
	var f float64
	fmt.Sscanf(strings.TrimSpace(s), "%g", &f)
	return f
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// removeOrphanedFiles deletes stored attachment files whose last database row
// has gone. Attachment filenames are content-addressed, so the same bytes can
// back several rows; a file is only removed once nothing references it.
func (s *Server) removeOrphanedFiles(storedNames []string) {
	for _, name := range storedNames {
		var remaining int64
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM receipts WHERE stored_name = ?`, name).Scan(&remaining); err != nil {
			log.Printf("check attachment references for %s: %v", name, err)
			continue
		}
		if remaining > 0 {
			continue
		}
		if err := os.Remove(filepath.Join(s.attachmentsDir(), filepath.Base(name))); err != nil && !os.IsNotExist(err) {
			log.Printf("remove attachment %s: %v", name, err)
		}
	}
}
