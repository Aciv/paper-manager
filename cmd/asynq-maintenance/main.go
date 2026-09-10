// asynq-maintenance reports and, when explicitly requested, removes task
// records left by task types retired during a breaking pipeline cutover.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"

	"paperMaster/internal/router"
	"paperMaster/internal/types"

	"github.com/hibiken/asynq"
)

var legacyTaskTypes = map[string]struct{}{
	"document:process":         {},
	"document:remote_import":   {},
	"paper:metadata-extract":   {},
	"paper:reference-dispatch": {},
	"paper:catalog-resolve":    {},
	"document:remote-import":   {},
}

var queues = []string{
	types.QueueCritical,
	types.QueueDefault,
	types.QueueLow,
	"download", // retired remote-reference queue; no runtime producer uses it.
}

const pageSize = 100

type taskLister func(string, ...asynq.ListOption) ([]*asynq.TaskInfo, error)

type taskState struct {
	name   string
	list   taskLister
	active bool
}

func main() {
	purge := flag.Bool("purge", false, "remove retired tasks; the default only reports them")
	flag.Parse()

	if os.Getenv("REDIS_ADDR") == "" {
		fmt.Fprintln(os.Stderr, "REDIS_ADDR is required")
		os.Exit(2)
	}

	inspector := router.NewAsynqInspector()
	defer inspector.Close()
	states := []taskState{
		{name: "pending", list: inspector.ListPendingTasks},
		{name: "scheduled", list: inspector.ListScheduledTasks},
		{name: "retry", list: inspector.ListRetryTasks},
		{name: "archived", list: inspector.ListArchivedTasks},
		{name: "completed", list: inspector.ListCompletedTasks},
		{name: "active", list: inspector.ListActiveTasks, active: true},
	}

	counts := make(map[string]int)
	seen := make(map[string]struct{})
	failed := false
	for _, queue := range queues {
		for _, state := range states {
			for page := 1; ; {
				tasks, err := state.list(queue, asynq.PageSize(pageSize), asynq.Page(page))
				if err != nil {
					if errors.Is(err, asynq.ErrQueueNotFound) {
						break
					}
					fmt.Fprintf(os.Stderr, "list %s tasks in %s: %v\n", state.name, queue, err)
					failed = true
					break
				}
				removed := false
				for _, task := range tasks {
					if _, legacy := legacyTaskTypes[task.Type]; !legacy {
						continue
					}
					identity := queue + "/" + state.name + "/" + task.ID
					if _, recorded := seen[identity]; !recorded {
						seen[identity] = struct{}{}
						counts[queue+"/"+state.name+"/"+task.Type]++
					}
					if !*purge || state.name == "completed" {
						continue
					}
					if state.active {
						err = inspector.CancelProcessing(task.ID)
					} else {
						err = inspector.DeleteTask(queue, task.ID)
					}
					if err != nil {
						fmt.Fprintf(os.Stderr, "remove %s task %s (%s): %v\n", state.name, task.ID, task.Type, err)
						failed = true
						continue
					}
					// Active tasks cannot be removed from Asynq's active set.
					// Cancellation is a signal to the worker, so do not restart
					// this page waiting for the worker to acknowledge it.
					removed = !state.active
				}
				if removed {
					// Deletion compacts the list/ZSET, so restart this page to
					// avoid skipping the entries that shifted into its range.
					continue
				}
				if len(tasks) < pageSize {
					break
				}
				page++
			}
		}
	}

	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Printf("%s=%d\n", key, counts[key])
	}
	if *purge {
		fmt.Println("completed tasks are intentionally reported but retained until Asynq retention expires")
	}
	if failed {
		os.Exit(1)
	}
}
