package api

import (
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	gophoto "github.com/anchoo2kewl/go-photo"

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

// photoOptions is how every upload is processed: downscaled to a long edge a
// photographed test sheet is still readable at, re-encoded, and — for anything
// that is not a decodable image, a PDF invoice say — kept as it arrived rather
// than refused, since losing somebody's receipt is worse than storing bytes we
// did not parse.
var photoOptions = gophoto.Options{
	MaxEdge:         maxImageDimension,
	Quality:         jpegQuality,
	MaxBytes:        maxUploadBytes,
	KeepUnsupported: true,
}

// attachmentsDir is where uploaded files live, under the data directory.
func (s *Server) attachmentsDir() string { return filepath.Join(s.Cfg.DataDir, "attachments") }

// photoStore is the go-photo store the attachments live in. It is created on
// first use so a server that never receives an upload never makes the
// directory.
func (s *Server) photoStore() (*gophoto.Store, error) {
	s.photoOnce.Do(func() {
		s.photos, s.photoErr = gophoto.NewStore(s.attachmentsDir(), photoOptions)
	})
	return s.photos, s.photoErr
}

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

	kind := strings.ToLower(strings.TrimSpace(r.FormValue("kind")))
	if kind != "test_sheet" {
		kind = "receipt"
	}

	saved, err := s.storeUpload(file, header.Filename)
	if err != nil {
		writeUploadError(w, err)
		return
	}

	rec := &store.Receipt{
		PoolID: pool.ID, UserID: &u.ID, Filename: header.Filename, StoredName: saved.RelPath,
		ContentType: saved.ContentType, SizeBytes: saved.Bytes(), OriginalBytes: saved.OriginalBytes,
		Width: int64(saved.Width), Height: int64(saved.Height), Kind: kind,
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

	created, err := s.fileAttachment(rec, saved)
	if err != nil {
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

// storeUpload runs an upload through go-photo and writes it under a
// content-addressed name, so identical uploads share one file on disk.
func (s *Server) storeUpload(r io.Reader, filename string) (*gophoto.Saved, error) {
	ps, err := s.photoStore()
	if err != nil {
		return nil, err
	}
	return ps.Save(r, filename, gophoto.ContentAddressed(""))
}

// fileAttachment records an upload, dealing with the one way the content
// addressing can bite.
//
// Files are named by the hash of their bytes, and stored_name is unique, so
// uploading something already on record would land on a name a row already
// holds — most often the same test sheet being filed against a second test.
// The bytes are identical either way; what the new record needs is a name of
// its own, so the two can be deleted independently.
//
// The collision is checked for rather than caught: the embedded engine reports
// a constraint violation as a bare "unknown error", which is not something to
// hang behaviour on.
func (s *Server) fileAttachment(rec *store.Receipt, saved *gophoto.Saved) (*store.Receipt, error) {
	if s.storedNameTaken(rec.StoredName) {
		ps, err := s.photoStore()
		if err != nil {
			return nil, err
		}
		copied, err := ps.SaveImage(saved.Image, func(img *gophoto.Image) string {
			return fmt.Sprintf("%s-%d%s", img.SHA256, time.Now().UnixNano(), img.Extension())
		})
		if err != nil {
			return nil, err
		}
		rec.StoredName = copied.RelPath
	}

	created, err := s.DB.CreateReceipt(rec)
	if err != nil {
		// Nothing points at the file, so it would otherwise sit on disk
		// forever with no row naming it.
		s.removeOrphanedFiles([]string{rec.StoredName})
		return nil, err
	}
	return created, nil
}

// storedNameTaken reports whether a record already claims this stored file.
func (s *Server) storedNameTaken(name string) bool {
	var n int64
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM receipts WHERE stored_name = ?`, name).Scan(&n); err != nil {
		log.Printf("check stored name %s: %v", name, err)
		return false
	}
	return n > 0
}

// writeUploadError maps go-photo's failures onto statuses a caller can act on.
func writeUploadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gophoto.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("that file is larger than the %d MB limit", maxUploadBytes>>20))
	case errors.Is(err, gophoto.ErrUnsupportedType):
		writeError(w, http.StatusBadRequest, "that file is not an image this can read")
	default:
		log.Printf("store upload: %v", err)
		writeError(w, http.StatusInternalServerError, "could not store the file")
	}
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
	// The store owns the containment check, so a crafted stored_name cannot
	// escape the attachments directory however it got into the database.
	ps, err := s.photoStore()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the file")
		return
	}
	f, err := ps.Open(rec.StoredName)
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
	s.removeOrphanedFiles([]string{storedName})
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
		ps, err := s.photoStore()
		if err != nil {
			log.Printf("open attachment store: %v", err)
			return
		}
		if err := ps.Remove(name); err != nil {
			log.Printf("remove attachment %s: %v", name, err)
		}
	}
}
