package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"paperMaster/internal/types"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type document struct {
	ID       uuid.UUID
	Revision int64
	Sections []types.ReadingNoteSectionEntry
	Expected map[uuid.UUID]string
}

type suite struct {
	o              options
	db             *gorm.DB
	client         *http.Client
	baseID         uuid.UUID
	docs           []*document
	verifiedWrites int64
}

func (s *suite) createBase() error {
	s.baseID = uuid.New()
	ctx, cancel := operationContext()
	defer cancel()
	return s.db.WithContext(ctx).Create(&types.DocumentBase{
		ID: s.baseID, Name: "loadtest-" + s.o.RunID, Settings: types.JSON(`{}`),
	}).Error
}

// tinyPDF is valid PDF 1.4 with one page, padded after EOF to the requested size.
// It is a transfer/storage fixture, not a representative parsing benchmark.
func tinyPDF(tag string, size int) []byte {
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	text := "BT /F1 12 Tf 72 720 Td (Load test fixture " + tag + ") Tj ET\n"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(text), text),
	}
	offsets := []int{0}
	for i, object := range objects {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := out.Len()
	out.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	if out.Len() < size {
		out.Write(bytes.Repeat([]byte(" "), size-out.Len()))
	}
	return out.Bytes()
}

func (s *suite) grow(size int) error {
	// Bounded parallel setup is unmeasured and always uses real note-import APIs.
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []error
	created := make([]*document, size-len(s.docs))
	start := len(s.docs)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				doc, err := s.seed(index)
				mu.Lock()
				if err != nil {
					failures = append(failures, err)
				} else {
					created[index-start] = doc
				}
				mu.Unlock()
			}
		}()
	}
	for i := start; i < size; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if len(failures) > 0 {
		return fmt.Errorf("seed failed (%d): %w", len(failures), failures[0])
	}
	s.docs = append(s.docs, created...)
	return nil
}

func (s *suite) seed(index int) (*document, error) {
	id := uuid.New()
	key := "pdf/" + id.String() + ".pdf"
	pdf := tinyPDF(id.String(), s.o.PDFBytes)
	if err := os.MkdirAll(filepath.Join(s.o.Storage, "pdf"), 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(s.o.Storage, key), pdf, 0600); err != nil {
		return nil, err
	}
	var md strings.Builder
	var blocks []types.Block
	var outline []types.DocumentOutlineSection
	for i := 0; i < s.o.Blocks; i++ {
		begin := md.Len()
		text := fmt.Sprintf("Document %d block %d: %s\n\n", index, i, strings.Repeat("method evidence result ", 12))
		md.WriteString(text)
		page, start, end := i/20+1, begin, md.Len()
		w, h, x0, y0, x1, y1 := 612.0, 792.0, 50.0, float64(i%20*30+30), 550.0, float64(i%20*30+50)
		blocks = append(blocks, types.Block{ID: uuid.New(), DocumentID: id, Ordinal: i, Type: "text", Text: text,
			Location: &types.PDFBlockLocation{PageNumber: &page, PageWidth: &w, PageHeight: &h,
				BBoxX0: &x0, BBoxY0: &y0, BBoxX1: &x1, BBoxY1: &y1, MarkdownCodepointStart: &start, MarkdownCodepointEnd: &end}})
		if i%20 == 0 {
			outline = append(outline, types.DocumentOutlineSection{Ordinal: len(outline), Title: fmt.Sprintf("Section %d", page),
				Level: 1, ParentOrdinal: -1, StartPage: page, EndPage: page, StartBlockOrdinal: i, EndBlockOrdinal: i + 19,
				MarkdownCodepointStart: begin})
		}
		last := &outline[len(outline)-1]
		last.MarkdownCodepointEnd = md.Len()
		last.EndBlockOrdinal = i
	}
	if err := os.MkdirAll(filepath.Join(s.o.Storage, "markdown"), 0700); err != nil {
		return nil, err
	}
	markdownKey := "markdown/" + id.String() + ".md"
	if err := os.WriteFile(filepath.Join(s.o.Storage, markdownKey), []byte(md.String()), 0600); err != nil {
		return nil, err
	}
	structure, err := (types.DocumentOutline{SchemaVersion: 1, Source: types.DocumentOutlinePDF, Sections: outline}).JSON()
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(pdf)
	doc := types.Document{
		ID: id, OriginalFilename: fmt.Sprintf("loadtest-%04d.pdf", index), ContentType: "application/pdf",
		SizeBytes: int64(len(pdf)), FileSHA256: fmt.Sprintf("%x", hash),
		StorageKey: key, SourceState: types.DocumentSourceAvailable,
		MarkdownStatus: types.DocumentStageSucceeded, MarkdownGeneration: 1,
		MetadataExtractionStatus: types.DocumentStageSkipped, SourceLookupStatus: types.DocumentStageSkipped,
		ReferenceLookupStatus: types.DocumentStageSkipped}
	ctx, cancel := operationContext()
	defer cancel()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, value := range []any{
			&doc,
			&types.DocumentMarkdown{
				DocumentID: id, StorageKey: markdownKey, Filename: "fixture.md", ContentType: "text/markdown",
				SizeBytes: int64(md.Len()), PageCount: (s.o.Blocks + 19) / 20, Metadata: types.JSON(`{}`),
				DocumentOutline: structure, RecognitionGeneration: 1},
			&blocks,
			&types.PaperMetadata{DocumentID: id, Title: fmt.Sprintf("Loadtest paper %04d", index),
				Authors: []types.PaperAuthor{{Name: "Fixture Author"}}, Tags: []string{"loadtest"},
				Metadata: types.JSON(`{}`), FieldSources: types.PaperMetadataFieldSources{}},
			&types.DocumentBaseFile{DocumentBaseID: s.baseID, DocumentID: id, SortOrder: index, Metadata: types.JSON(`{}`)},
		} {
			if err := tx.Create(value).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var note strings.Builder
	for i := 0; i < s.o.Sections; i++ {
		note.WriteString(sectionBody(i, "initial", s.o.SectionBytes))
	}
	status, data, err := s.request("PUT", "/api/document/"+id.String()+"/reading-note?expectedRevision=0",
		[]byte(note.String()), "text/markdown", nil)
	if err != nil || status != 200 {
		return nil, fmt.Errorf("seed note HTTP %d: %v: %.300s", status, err, data)
	}
	var saved types.DocumentReadingNote
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, err
	}
	if len(saved.Directory.Sections) != s.o.Sections {
		return nil, fmt.Errorf("seed note section count mismatch")
	}
	return &document{ID: id, Revision: saved.Revision, Sections: saved.Directory.Sections,
		Expected: map[uuid.UUID]string{}}, nil
}

func sectionBody(index int, marker string, size int) string {
	heading := fmt.Sprintf("## Note section %02d\n\n", index)
	line := "Evidence, method details, comparison and measured results. 注释验证。\n"
	return heading + marker + "\n" + strings.Repeat(line, size/len(line)) + "\n"
}

func (s *suite) request(method, path string, body []byte, kind string, headers map[string]string) (int, []byte, error) {
	req, err := http.NewRequest(method, s.o.API+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if kind != "" {
		req.Header.Set("Content-Type", kind)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return resp.StatusCode, data, err
}
