package main

import (
	"testing"
)

func TestWriterPartitionsDoNotOverlapAndCoverCorpus(t *testing.T) {
	for documents := 1; documents <= 1000; documents++ {
		for _, clients := range []int{1, 4, 8, 16, 32} {
			writers := min(clients, documents)
			seen := make(map[int]int, documents)
			for worker := 0; worker < writers; worker++ {
				count := (documents-1-worker)/writers + 1
				for iteration := 0; iteration < count; iteration++ {
					id := ownedDocument(worker, iteration, writers, documents)
					if id < 0 || id >= documents {
						t.Fatalf("out of bounds: %d/%d", id, documents)
					}
					if owner, ok := seen[id]; ok {
						t.Fatalf("document %d shared by writers %d and %d", id, owner, worker)
					}
					seen[id] = worker
				}
				if ownedDocument(worker, count, writers, documents) != worker {
					t.Fatal("partition did not wrap")
				}
			}
			if len(seen) != documents {
				t.Fatal("uncovered document")
			}
		}
	}
}

func TestLatencyQuantilesAndMatrixValidation(t *testing.T) {
	var values []float64
	for n := 100; n >= 1; n-- {
		values = append(values, float64(n))
	}
	got := percentiles(values)
	if got.P50 != 50 || got.P95 != 95 || got.P99 != 99 || got.Max != 100 {
		t.Fatalf("incorrect quantiles: %+v", got)
	}
	if percentiles(nil) != (latency{}) {
		t.Fatal("empty samples not handled")
	}
	for _, invalid := range []string{"", "0", "1001", "1,1", "10,1", "x"} {
		if _, err := numbers(invalid); err == nil {
			t.Fatalf("invalid matrix accepted: %s", invalid)
		}
	}
	if got, err := numbers("1,10,500,1000"); err != nil || len(got) != 4 {
		t.Fatalf("valid scale rejected: %v", err)
	}
}
