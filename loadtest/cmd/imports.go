package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"paperMaster/internal/types"
)

type importResult struct {
	SourceRowsBefore, SourceRowsAfter                              int64
	Name                                                           string
	Total, Accepted, Duplicate, Rejected, Failed, ExpectedFailures int
	Acceptance                                                     latency
	CompletionSeconds                                              float64
	ProcessingStates                                               map[string]int
	Background                                                     roundResult
}

type importBatch struct {
	Result   types.DocumentBatchResult
	AcceptMS float64
	Error    error
}

func (s *suite) imports(report *report) error {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "missing") {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(r.URL.Path, "slow") {
			time.Sleep(150 * time.Millisecond)
		}
		data := tinyPDF("remote-"+r.URL.Path, s.o.PDFBytes)
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		_, _ = w.Write(data)
	}))
	defer server.Close()
	for _, name := range []string{
		"files_unique", "files_duplicate", "urls_unique", "urls_duplicate", "urls_partial_slow",
	} {
		fmt.Println("Import scenario:", name)
		var rowsBefore int64
		ctx, cancel := operationContext()
		if err := s.db.WithContext(ctx).Table("documents").Count(&rowsBefore).Error; err != nil {
			cancel()
			return err
		}
		cancel()
		start := time.Now()
		background := make(chan roundResult, 1)
		go func() { background <- s.round(min(16, s.o.Concurrency[len(s.o.Concurrency)-1]), 8*time.Second) }()
		out := make(chan importBatch, s.o.ImportBatches)
		var wg sync.WaitGroup
		for batch := 0; batch < s.o.ImportBatches; batch++ {
			wg.Add(1)
			go func(batch int) { defer wg.Done(); out <- s.importBatch(name, batch, server.URL) }(batch)
		}
		wg.Wait()
		close(out)
		row := importResult{Name: name, Total: s.o.ImportBatches * 20, ProcessingStates: map[string]int{}}
		var delays []float64
		var documents []string
		var problems []string
		for batch := range out {
			delays = append(delays, batch.AcceptMS)
			if batch.Error != nil {
				problems = append(problems, batch.Error.Error())
				continue
			}
			if len(batch.Result.Items) != 20 || batch.Result.Stopped {
				problems = append(problems, "incomplete batch")
			}
			for _, item := range batch.Result.Items {
				switch item.Status {
				case types.DocumentImportItemAccepted:
					row.Accepted++
					if item.ProcessingQueued == nil || !*item.ProcessingQueued {
						problems = append(problems, "accepted source was not queued")
					}
				case types.DocumentImportItemDuplicate:
					row.Duplicate++
				case types.DocumentImportItemRejected:
					row.Rejected++
				case types.DocumentImportItemFailed:
					row.Failed++
				default:
					problems = append(problems, "unknown item status")
				}
				if item.Document != nil {
					documents = append(documents, item.Document.ID.String())
				}
			}
		}
		row.CompletionSeconds = time.Since(start).Seconds()
		row.SourceRowsBefore = rowsBefore
		ctx, cancel = operationContext()
		if err := s.db.WithContext(ctx).Table("documents").Count(&row.SourceRowsAfter).Error; err != nil {
			problems = append(problems, err.Error())
		}
		cancel()
		if row.SourceRowsAfter-row.SourceRowsBefore != int64(row.Accepted) {
			problems = append(problems, "source row count does not match accepted items")
		}
		row.Acceptance = percentiles(delays)
		row.Background = <-background
		if err := s.verifyNotes(); err != nil {
			problems = append(problems, err.Error())
		}
		if row.Background.Errors > 0 {
			problems = append(problems, "background notes/PDF requests failed")
		}
		if name == "urls_partial_slow" {
			row.ExpectedFailures = s.o.ImportBatches
		}
		if row.Total != row.Accepted+row.Duplicate+row.Rejected+row.Failed {
			problems = append(problems, "item count mismatch")
		}
		if row.Failed+row.Rejected != row.ExpectedFailures {
			problems = append(problems, "unexpected import failures")
		}
		if strings.HasSuffix(name, "duplicate") && row.Duplicate != row.Total {
			problems = append(problems, "duplicate inputs created or rejected documents")
		}
		if len(documents) > 0 {
			var states []struct {
				MarkdownStatus string
				Count          int
			}
			ctx, cancel := operationContext()
			err := s.db.WithContext(ctx).Table("documents").Select("markdown_status, count(*) AS count").
				Where("id IN ?", documents).Group("markdown_status").Scan(&states).Error
			cancel()
			if err != nil {
				problems = append(problems, err.Error())
			}
			for _, state := range states {
				row.ProcessingStates[state.MarkdownStatus] = state.Count
			}
		}
		report.Imports = append(report.Imports, row)
		for _, problem := range problems {
			report.CorrectnessErrors = append(report.CorrectnessErrors, name+": "+problem)
		}
		fmt.Printf("%s completed %.2fs: accepted=%d duplicate=%d rejected=%d failed=%d\n",
			name, row.CompletionSeconds, row.Accepted, row.Duplicate, row.Rejected, row.Failed)
		if err := saveReport(s.o.Output, *report); err != nil {
			return err
		}
	}
	return nil
}

func (s *suite) importBatch(name string, batch int, origin string) importBatch {
	at := time.Now()
	var status int
	var data []byte
	var err error
	var result types.DocumentBatchResult
	var acceptMS float64
	if strings.HasPrefix(name, "files_") {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		for item := 0; item < 20; item++ {
			part, e := form.CreateFormFile("files", fmt.Sprintf("import-%d-%d.pdf", batch, item))
			if e != nil {
				return importBatch{Error: e}
			}
			_, _ = part.Write(tinyPDF(fmt.Sprintf("file-%s-%d-%d", s.o.RunID, batch, item), s.o.PDFBytes))
		}
		if err := form.Close(); err != nil {
			return importBatch{Error: err}
		}
		at = time.Now()
		status, data, err = s.request("POST", "/api/document/batch-upload", body.Bytes(), form.FormDataContentType(), nil)
		acceptMS = float64(time.Since(at).Microseconds()) / 1000
		if err == nil && status == 200 {
			err = json.Unmarshal(data, &result)
		} else if err == nil {
			err = fmt.Errorf("file batch HTTP %d: %.200s", status, data)
		}
	} else {
		var urls []types.DocumentURLImportCommand
		for item := 0; item < 20; item++ {
			prefix := "normal"
			if name == "urls_partial_slow" {
				prefix = "partial"
				if item == 0 {
					prefix = "missing"
				}
				if item == 1 {
					prefix = "slow"
				}
			}
			urls = append(urls, types.DocumentURLImportCommand{
				URL: fmt.Sprintf("%s/%s-%s-%d-%d.pdf", origin, prefix, s.o.RunID, batch, item)})
		}
		body, _ := json.Marshal(map[string]any{
			"urls": urls, "baseIds": []string{s.baseID.String()}, "requestId": uuid.NewString(),
		})
		status, data, err = s.request("POST", "/api/document/url-imports", body, "application/json", nil)
		acceptMS = float64(time.Since(at).Microseconds()) / 1000
		if err == nil && status != 202 {
			err = fmt.Errorf("URL enqueue HTTP %d: %.200s", status, data)
		}
		var job types.URLImportTask
		if err == nil {
			err = json.Unmarshal(data, &job)
		}
		deadline := time.Now().Add(150 * time.Second)
		for err == nil && time.Now().Before(deadline) {
			status, data, err = s.request("GET", "/api/document/url-imports/"+job.ID, nil, "", nil)
			if err != nil {
				break
			}
			if status != 200 {
				err = fmt.Errorf("URL status HTTP %d", status)
				break
			}
			if err = json.Unmarshal(data, &job); err != nil {
				break
			}
			if job.State == "succeeded" || job.State == "partial" || job.State == "failed" || job.State == "stopped" {
				if job.Result == nil {
					err = fmt.Errorf("terminal URL task has no results")
				} else {
					result = *job.Result
				}
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err == nil && len(result.Items) == 0 {
			err = fmt.Errorf("URL job did not complete within 150s")
		}
	}
	return importBatch{Result: result, AcceptMS: acceptMS, Error: err}
}
