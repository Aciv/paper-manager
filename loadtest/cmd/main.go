// Command loadtest exercises the real API against a disposable make dev stack.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
)

type options struct {
	API, Storage, Output, RunID                             string
	Sizes, Concurrency                                      []int
	Duration, Warmup                                        time.Duration
	Blocks, Sections, SectionBytes, PDFBytes, ImportBatches int
}

func numbers(raw string) ([]int, error) {
	var values []int
	last := 0
	for _, item := range strings.Split(raw, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(item))
		if err != nil || n <= last || n > 1000 {
			return nil, fmt.Errorf("values must increase within 1–1000: %q", raw)
		}
		values = append(values, n)
		last = n
	}
	return values, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var o options
	var sizes, concurrency string
	flag.StringVar(&o.API, "api", "", "isolated API URL")
	flag.StringVar(&o.Storage, "storage", "", "isolated UPLOAD_DIR")
	flag.StringVar(&o.Output, "output", "", "result directory")
	flag.StringVar(&sizes, "sizes", "1,10,50,100,250,500,1000", "growing database corpus sizes")
	flag.StringVar(&concurrency, "concurrency", "1,4,8,16,32", "simultaneous HTTP client workflows")
	flag.DurationVar(&o.Duration, "duration", 8*time.Second, "measured duration per round")
	flag.DurationVar(&o.Warmup, "warmup", 2*time.Second, "unmeasured warmup per round")
	flag.IntVar(&o.Blocks, "blocks", 200, "blocks per document")
	flag.IntVar(&o.Sections, "sections", 12, "note sections per document")
	flag.IntVar(&o.SectionBytes, "section-bytes", 8192, "approximate section bytes")
	flag.IntVar(&o.PDFBytes, "pdf-bytes", 1<<20, "source PDF bytes")
	flag.IntVar(&o.ImportBatches, "import-batches", 2, "parallel 20-item batches per import scenario; 0 skips")
	flag.Parse()
	var err error
	o.Sizes, err = numbers(sizes)
	if err != nil {
		return err
	}
	o.Concurrency, err = numbers(concurrency)
	if err != nil {
		return err
	}
	o.RunID = os.Getenv("LOADTEST_RUN_ID")
	if o.RunID == "" || os.Getenv("LOADTEST_ISOLATED") != "1" || o.Output == "" || o.Storage == "" {
		return fmt.Errorf("use loadtest/run.py to provision an isolated test stack")
	}
	if o.Duration < time.Second || o.Warmup < 0 || o.Blocks < 20 || o.Sections < 2 ||
		o.Sections > 128 || o.SectionBytes < 512 || o.SectionBytes > 65536 || o.PDFBytes < 1024 ||
		o.PDFBytes > 10<<20 || o.ImportBatches < 0 || o.ImportBatches > 10 {
		return fmt.Errorf("invalid workload bounds")
	}
	db, err := gorm.Open(postgres.Open(os.Getenv("DATABASE_URL")), &gorm.Config{
		Logger: gormlog.Default.LogMode(gormlog.Silent), CreateBatchSize: 200,
	})
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(2)
	var existing int64
	ctx, cancel := operationContext()
	err = db.WithContext(ctx).Table("documents").Count(&existing).Error
	cancel()
	if err != nil {
		return err
	}
	if existing != 0 {
		return fmt.Errorf("refusing fixture writes: database is not empty (%d documents)", existing)
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		MaxIdleConns:        max(256, o.Concurrency[len(o.Concurrency)-1]*2),
		MaxIdleConnsPerHost: max(128, o.Concurrency[len(o.Concurrency)-1]),
		MaxConnsPerHost:     max(256, o.Concurrency[len(o.Concurrency)-1]),
	}}
	defer client.CloseIdleConnections()
	s := &suite{o: o, db: db, client: client}
	if err := s.createBase(); err != nil {
		return err
	}
	report := report{RunID: o.RunID, StartedAt: time.Now().UTC(), Options: o,
		Boundary: "HTTP → production Go → real PostgreSQL/Redis/files; synthetic parsed fixtures; no Python in core"}
	for _, size := range o.Sizes {
		fmt.Printf("Seeding corpus %d → %d documents\n", len(s.docs), size)
		if err := s.grow(size); err != nil {
			return err
		}
		ctx, cancel = operationContext()
		err = db.WithContext(ctx).Exec("ANALYZE").Error
		cancel()
		if err != nil {
			return err
		}
		for _, n := range o.Concurrency {
			if o.Warmup > 0 {
				warm := s.round(n, o.Warmup)
				if warm.Errors > 0 {
					return fmt.Errorf("warmup failed: %v", warm.ErrorSamples)
				}
			}
			result := s.round(n, o.Duration)
			fmt.Printf("corpus=%d clients=%d requests=%d rps=%.1f errors=%d p95=%.2fms\n",
				size, n, result.Requests, result.RPS, result.Errors, result.Latency.P95)
			beforeVerify := s.verifiedWrites
			if err := s.verifyNotes(); err != nil {
				report.CorrectnessErrors = append(report.CorrectnessErrors, err.Error())
			}
			result.VerifiedSections = int(s.verifiedWrites - beforeVerify)
			report.Rounds = append(report.Rounds, result)
			if err := saveReport(o.Output, report); err != nil {
				return err
			}
		}
	}
	if o.ImportBatches > 0 {
		if err := s.imports(&report); err != nil {
			report.CorrectnessErrors = append(report.CorrectnessErrors, err.Error())
		}
	}
	report.FinishedAt = time.Now().UTC()
	if err := saveReport(o.Output, report); err != nil {
		return err
	}
	if len(report.CorrectnessErrors) > 0 {
		return fmt.Errorf("correctness validation failed; see results.json")
	}
	for _, r := range report.Rounds {
		if r.Errors > 0 {
			return fmt.Errorf("unexpected HTTP failures; see results.json")
		}
	}
	return nil
}

func saveReport(dir string, value report) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "results.json"), append(data, '\n'), 0600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "summary.md"), []byte(value.markdown()), 0600)
}

// Context is retained in every database call made by the fixture driver.
func operationContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}
