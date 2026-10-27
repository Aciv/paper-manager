package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

type sample struct {
	Operation    string
	Milliseconds float64
	Status       int
	Bytes        int
	Error        string
}

type latency struct{ P50, P95, P99, Max float64 }
type operationResult struct {
	Requests, Errors int
	RPS              float64
	Latency          latency
	Statuses         map[int]int
}
type dbSnapshot struct {
	Commits, Rollbacks, BlocksRead, BlocksHit, RowsReturned, RowsFetched, TempBytes, Deadlocks int64
}
type roundResult struct {
	StartedAt, FinishedAt                                           time.Time
	Documents, Clients, Writers, Requests, Errors, VerifiedSections int
	Seconds, RPS                                                    float64
	Latency                                                         latency
	Operations                                                      map[string]operationResult
	ErrorSamples                                                    []string
	DBBefore, DBAfter                                               dbSnapshot
	DBMaxActive, DBMaxLockWait                                      int
}

func (s *suite) databaseSnapshot() dbSnapshot {
	ctx, cancel := operationContext()
	defer cancel()
	var row dbSnapshot
	s.db.WithContext(ctx).Raw(`SELECT xact_commit AS commits, xact_rollback AS rollbacks,
		blks_read AS blocks_read, blks_hit AS blocks_hit, tup_returned AS rows_returned,
		tup_fetched AS rows_fetched, temp_bytes, deadlocks FROM pg_stat_database
		WHERE datname=current_database()`).Scan(&row)
	return row
}

func (s *suite) round(clients int, duration time.Duration) roundResult {
	result := roundResult{Documents: len(s.docs), Clients: clients,
		Operations: map[string]operationResult{}, DBBefore: s.databaseSnapshot()}
	writers := min(clients, len(s.docs))
	result.Writers = writers
	// One writer owns each document partition; readers may overlap any document.
	// There is deliberately no same-note competing writer in this main workload.
	end := time.Now().Add(duration)
	start := time.Now()
	result.StartedAt = start
	var all []sample
	var mu sync.Mutex
	var wg sync.WaitGroup
	stopDB := make(chan struct{})
	var dbWG sync.WaitGroup
	dbWG.Add(1)
	go func() {
		defer dbWG.Done()
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopDB:
				return
			case <-ticker.C:
				var counts struct{ Active, Waiting int }
				ctx, cancel := operationContext()
				s.db.WithContext(ctx).Raw(`SELECT count(*) FILTER (WHERE state='active') AS active,
				count(*) FILTER (WHERE wait_event_type='Lock') AS waiting
				FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()`).Scan(&counts)
				cancel()
				result.DBMaxActive = max(result.DBMaxActive, counts.Active)
				result.DBMaxLockWait = max(result.DBMaxLockWait, counts.Waiting)
			}
		}
	}()
	for worker := 0; worker < clients; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			var local []sample
			counter := 0
			seed := uint64(worker+1) * 991
			for time.Now().Before(end) {
				seed = seed*6364136223846793005 + 1
				doc := s.docs[int(seed>>32)%len(s.docs)]
				var op, path, kind string
				method := "GET"
				var body []byte
				var headers map[string]string
				sectionIndex := counter % s.o.Sections
				marker := ""
				if worker < writers && counter%4 == 0 {
					owned := ownedDocument(worker, counter/4, writers, len(s.docs))
					sectionIndex = (counter / 4) % s.o.Sections
					doc = s.docs[owned]
					op = "write_note_section"
					method = "PUT"
					kind = "text/markdown"
					marker = fmt.Sprintf("loadtest-%s-%d-%d-%d", s.o.RunID, worker, counter, time.Now().UnixNano())
					body = []byte(sectionBody(sectionIndex, marker, s.o.SectionBytes))
					path = fmt.Sprintf("/api/document/%s/reading-note/sections/%s?expectedRevision=%d",
						doc.ID, doc.Sections[sectionIndex].ID, doc.Revision)
				} else {
					root := "/api/document/" + doc.ID.String()
					switch (counter + worker) % 9 {
					case 0:
						op = "read_note_directory"
						path = root + "/reading-note"
					case 1:
						op = "read_note_section"
						path = root + "/reading-note/sections/" + doc.Sections[sectionIndex].ID.String()
					case 2:
						op = "paper_blocks_page"
						path = root + "/blocks?page=1"
					case 3:
						op = "paper_structure"
						path = root + "/structure"
					case 4:
						op = "paper_metadata"
						path = "/api/paper/" + doc.ID.String()
					case 5:
						op = "pdf_range"
						path = root + "/content"
						headers = map[string]string{"Range": "bytes=0-65535"}
					case 6:
						op = "base_list"
						path = fmt.Sprintf("/api/document/bases/%s/documents?page=1&pageSize=20", s.baseID)
					case 7:
						op = "source_markdown"
						path = root + "/markdown"
					case 8:
						op = "paper_search"
						path = "/api/paper/search?title=" + url.QueryEscape("Loadtest paper")
					}
				}
				at := time.Now()
				status, data, err := s.request(method, path, body, kind, headers)
				item := sample{Operation: op, Milliseconds: float64(time.Since(at).Microseconds()) / 1000,
					Status: status, Bytes: len(data)}
				if err != nil {
					item.Error = err.Error()
				} else if status != 200 && !(op == "pdf_range" && status == 206) {
					item.Error = fmt.Sprintf("HTTP %d", status)
				} else if op == "write_note_section" {
					var edited types.ReadingNoteEditResult
					if err := json.Unmarshal(data, &edited); err != nil {
						item.Error = err.Error()
					} else if edited.Revision != doc.Revision+1 || edited.DocumentID != doc.ID {
						item.Error = "write returned incorrect revision or document"
					} else {
						doc.Revision = edited.Revision
						doc.Expected[doc.Sections[sectionIndex].ID] = marker
					}
				} else if op == "read_note_section" {
					var section types.ReadingNoteSection
					if err := json.Unmarshal(data, &section); err != nil {
						item.Error = err.Error()
					} else if section.DocumentID != doc.ID || section.Section.ID != doc.Sections[sectionIndex].ID ||
						!strings.HasPrefix(section.Markdown, "## Note section") {
						item.Error = "incorrect note read content or identity"
					}
				} else if op == "pdf_range" && (!strings.HasPrefix(string(data), "%PDF-") || len(data) != 65536) {
					item.Error = "incorrect PDF range response"
				}
				local = append(local, item)
				counter++
			}
			mu.Lock()
			all = append(all, local...)
			mu.Unlock()
		}(worker)
	}
	wg.Wait()
	close(stopDB)
	dbWG.Wait()
	result.FinishedAt = time.Now()
	result.Seconds = result.FinishedAt.Sub(start).Seconds()
	result.DBAfter = s.databaseSnapshot()
	result.Requests = len(all)
	result.RPS = float64(len(all)) / result.Seconds
	var elapsed []float64
	byOperation := map[string][]sample{}
	for _, item := range all {
		elapsed = append(elapsed, item.Milliseconds)
		byOperation[item.Operation] = append(byOperation[item.Operation], item)
		if item.Error != "" {
			result.Errors++
			if len(result.ErrorSamples) < 10 {
				result.ErrorSamples = append(result.ErrorSamples, item.Operation+": "+item.Error)
			}
		}
	}
	result.Latency = percentiles(elapsed)
	for op, items := range byOperation {
		row := operationResult{Requests: len(items), RPS: float64(len(items)) / result.Seconds, Statuses: map[int]int{}}
		elapsed = nil
		for _, item := range items {
			row.Statuses[item.Status]++
			elapsed = append(elapsed, item.Milliseconds)
			if item.Error != "" {
				row.Errors++
			}
		}
		row.Latency = percentiles(elapsed)
		result.Operations[op] = row
	}
	return result
}

func (s *suite) verifyNotes() error {
	for _, doc := range s.docs {
		for sectionID, marker := range doc.Expected {
			path := fmt.Sprintf("/api/document/%s/reading-note/sections/%s", doc.ID, sectionID)
			status, data, err := s.request("GET", path, nil, "", nil)
			if err != nil || status != 200 {
				return fmt.Errorf("verify saved note HTTP %d: %v", status, err)
			}
			var section types.ReadingNoteSection
			if err := json.Unmarshal(data, &section); err != nil {
				return err
			}
			if section.Revision != doc.Revision || section.Section.ID != sectionID ||
				!strings.Contains(section.Markdown, marker) {
				return fmt.Errorf("acknowledged write lost: document %s section %s", doc.ID, sectionID)
			}
			s.verifiedWrites++
		}
		doc.Expected = map[uuid.UUID]string{}
	}
	return nil
}

func ownedDocument(worker, iteration, writers, documents int) int {
	return worker + (iteration%((documents-1-worker)/writers+1))*writers
}
