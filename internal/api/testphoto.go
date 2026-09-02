package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	gophoto "github.com/anchoo2kewl/go-photo"

	"github.com/biswas-dev/pool/internal/ai"
	"github.com/biswas-dev/pool/internal/store"
)

// handleCreateTestFromPhoto turns a photograph of a test sheet into a stored
// test, and optionally analyses it in the same request.
//
// This is the path that matters on a phone in the back garden: the pool store
// hands over a printout, and the whole of it — twenty readings, the date, who
// tested it — becomes a scored test with a dosing plan without anyone typing a
// number. Everything it does is reachable through the ordinary endpoints; what
// it saves is the twenty minutes of transcription between them.
//
// The order is deliberate. The photo is read before anything is written, so a
// picture of a sandwich creates no empty test to clean up. The photo is then
// filed as the test's sheet, so the readings can be checked against the paper
// they came from — which matters more here than anywhere else in the app,
// because a model transcribed them.
func (s *Server) handleCreateTestFromPhoto(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusBadRequest, "a file field with a photo of the test sheet is required")
		return
	}
	defer file.Close()

	pool, err := s.ownedPool(r, formInt(r, "pool_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	svc, err := s.aiService(u)
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err.Error())
		return
	}

	// Process before storing: the downscaled JPEG is both what gets filed and
	// what the model reads, so the two can never disagree about what was on
	// the paper — and it is a fraction of the tokens of a phone original.
	raw, err := readUpload(file, maxUploadBytes)
	if err != nil {
		writeUploadError(w, err)
		return
	}
	img, err := gophoto.Process(raw, header.Filename, sheetPhotoOptions)
	if err != nil {
		writeUploadError(w, err)
		return
	}

	// Reading a sheet is slower than a page load tolerates, and the caller has
	// already committed to waiting, so give it a budget of its own.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 150*time.Second)
	defer cancel()

	read, err := svc.ReadTestSheet(ctx, img.Data, img.ContentType, r.FormValue("hint"))
	if err != nil {
		if errors.Is(err, ai.ErrDisabled) {
			writeError(w, http.StatusPreconditionFailed, err.Error())
			return
		}
		// Nothing has been written yet, so an unreadable photo costs the
		// caller a retry and nothing else.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	test, err := s.testFromReading(u, pool, r, read)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// File the photo against the test it produced. A failure here is logged
	// rather than fatal: the readings are already saved, and losing the
	// picture is not worth losing them over.
	attachment := s.attachSheet(u, pool, test, img, header.Filename, read)

	out := s.testDetail(pool, test)
	out["parsed"] = read
	out["attachment"] = attachment

	// The analysis is the point of having entered the test at all, so it runs
	// here unless the caller asks it not to. If the model fails on this second
	// call the test still stands; the caller is told why there is no analysis
	// and can retry it on its own endpoint.
	if !isFalse(r.FormValue("analyse")) {
		if insight, err := svc.Analyse(ctx, mustPrompt(s, u, pool, test)); err != nil {
			out["insight_error"] = err.Error()
		} else if note, err := s.saveInsight(u, pool, test, insight); err != nil {
			log.Printf("save insight for test %d: %v", test.ID, err)
			out["insight"] = insight
		} else {
			out["insight"] = insight
			out["note"] = note
			// Re-read the notes so the caller's copy includes the one just
			// written, rather than showing an analysis the test appears not
			// to have.
			out["notes"], _ = s.DB.ListNotes(pool.ID, &test.ID)
		}
	}

	writeJSON(w, http.StatusCreated, out)
}

// sheetPhotoOptions differ from an ordinary attachment's: a test sheet is
// dense small print that a model has to read, so it is kept larger and at a
// higher quality than a receipt anybody only ever glances at. Anything that is
// not a decodable image is refused outright — there is nothing to send to a
// vision model.
var sheetPhotoOptions = gophoto.Options{
	MaxEdge:  2000,
	Quality:  88,
	MaxBytes: maxUploadBytes,
}

// testFromReading stores a transcribed sheet as a test, with the same
// derivation and dosing every other test gets.
func (s *Server) testFromReading(u *store.User, pool *store.Pool, r *http.Request, read *ai.SheetReading) (*store.Test, error) {
	// A date typed by the person wins over one read off the paper: they know
	// which day they are filing, and a misread year is the kind of mistake
	// that quietly reorders a whole season.
	testedAt := firstNonEmpty(r.FormValue("tested_at"), read.TestedAt)
	if stamp, err := normaliseTimestamp(testedAt); err == nil {
		testedAt = stamp
	} else {
		testedAt = store.Now()
	}

	t := &store.Test{
		PoolID:    pool.ID,
		TestedAt:  testedAt,
		Source:    "photo",
		Operator:  trimTo(read.Operator, 120),
		TestCount: read.TestCount,
	}
	applyReading(t, read.Values)

	if name := firstNonEmpty(r.FormValue("company_name"), read.Company); strings.TrimSpace(name) != "" {
		if c, err := s.DB.CompanyByName(u.ID, strings.TrimSpace(name), "store"); err == nil {
			t.CompanyID = &c.ID
		}
	}

	if err := s.derive(pool, t); err != nil {
		return nil, err
	}
	created, err := s.DB.CreateTest(t)
	if err != nil {
		return nil, err
	}
	if err := s.saveTreatments(pool, created); err != nil {
		return nil, err
	}

	// What the model could not read, and what was thrown out as impossible, is
	// recorded as a note rather than dropped: months later it is the only
	// explanation for why a row is blank.
	if note := transcriptionNote(read); note != "" {
		s.DB.CreateNote(&store.Note{
			TestID: &created.ID, PoolID: pool.ID, UserID: &u.ID, Kind: "human", Body: note,
		})
	}
	if extra := strings.TrimSpace(r.FormValue("notes")); extra != "" {
		s.DB.CreateNote(&store.Note{
			TestID: &created.ID, PoolID: pool.ID, UserID: &u.ID, Kind: "human", Body: extra,
		})
	}
	return created, nil
}

// applyReading copies transcribed values onto a test. Only the keys the model
// actually returned are set, so an untested row stays null rather than zero —
// the difference between "no stabilizer in the water" and "nobody measured".
func applyReading(t *store.Test, values map[string]float64) {
	set := func(dst **float64, key string) {
		if v, ok := values[key]; ok {
			val := v
			*dst = &val
		}
	}
	set(&t.FreeChlorine, "free_chlorine")
	set(&t.TotalChlorine, "total_chlorine")
	set(&t.CombinedChlorine, "combined_chlorine")
	set(&t.TotalSalt, "total_salt")
	set(&t.Bromine, "bromine")
	set(&t.PH, "ph")
	set(&t.TotalAlkalinity, "total_alkalinity")
	set(&t.CalciumHardness, "calcium_hardness")
	set(&t.CyanuricAcid, "cyanuric_acid")
	set(&t.Phosphate, "phosphate")
	set(&t.Borate, "borate")
	set(&t.TDS, "tds")
	set(&t.Temperature, "temperature")
	set(&t.TotalCopper, "total_copper")
	set(&t.FreeCopper, "free_copper")
	set(&t.CombinedCopper, "combined_copper")
	set(&t.Iron, "iron")
	set(&t.WQI, "wqi")
}

// transcriptionNote records what the transcription could not do, in the words
// of somebody reading it later.
func transcriptionNote(read *ai.SheetReading) string {
	var parts []string
	if n := strings.TrimSpace(read.Notes); n != "" {
		parts = append(parts, n)
	}
	if len(read.Rejected) > 0 {
		parts = append(parts, "Discarded as impossible for pool water: "+strings.Join(read.Rejected, ", ")+
			". Check these against the sheet and enter them by hand if they are real.")
	}
	if len(parts) == 0 {
		return ""
	}
	prefix := "Read from a photo"
	if read.Model != "" {
		prefix += " by " + read.Model
	}
	if read.Confidence > 0 {
		prefix += fmt.Sprintf(" (confidence %.0f%%)", read.Confidence*100)
	}
	return prefix + ". " + strings.Join(parts, " ")
}

// attachSheet files the photo the readings came from against the test.
func (s *Server) attachSheet(u *store.User, pool *store.Pool, test *store.Test, img *gophoto.Image, filename string, read *ai.SheetReading) *store.Receipt {
	ps, err := s.photoStore()
	if err != nil {
		log.Printf("open attachment store: %v", err)
		return nil
	}
	saved, err := ps.SaveImage(img, gophoto.ContentAddressed(""))
	if err != nil {
		log.Printf("store test sheet: %v", err)
		return nil
	}

	rec, err := s.fileAttachment(&store.Receipt{
		PoolID: pool.ID, UserID: &u.ID, TestID: &test.ID,
		Filename: orDefault(filename, "test-sheet.jpg"), StoredName: saved.RelPath,
		ContentType: saved.ContentType, SizeBytes: saved.Bytes(), OriginalBytes: saved.OriginalBytes,
		Width: int64(saved.Width), Height: int64(saved.Height),
		Kind: "test_sheet", Currency: "CAD",
		Notes: strings.TrimSpace("Source photo for this test. " + read.Notes),
	}, saved)
	if err != nil {
		log.Printf("record test sheet: %v", err)
		return nil
	}
	return rec
}

// mustPrompt builds the analysis prompt, degrading to a minimal one rather
// than failing the request: the test is already stored, and an analysis of
// less context beats no analysis and an error.
func mustPrompt(s *Server, u *store.User, pool *store.Pool, test *store.Test) string {
	prompt, err := s.buildPrompt(u, pool, test)
	if err != nil {
		log.Printf("build prompt for test %d: %v", test.ID, err)
		return fmt.Sprintf("POOL\nName: %s\nVolume: %.0f L\nSurface: %s\nSanitizer: %s\n\nCURRENT TEST (%s)\nThe history and logbook could not be loaded; analyse the readings alone.\n",
			pool.Name, pool.VolumeL, pool.Surface, pool.Sanitizer, test.TestedAt)
	}
	return prompt
}

// readUpload reads an upload, refusing one over the limit rather than
// truncating it into a corrupt image.
func readUpload(r io.Reader, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%w (limit %d bytes)", gophoto.ErrTooLarge, limit)
	}
	return raw, nil
}

// isFalse reads the off switch on a form field that defaults to on.
func isFalse(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "false", "0", "no", "off":
		return true
	}
	return false
}

// trimTo bounds a string a model produced before it reaches a column.
func trimTo(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n]
}
