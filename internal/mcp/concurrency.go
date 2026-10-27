package mcp

import "paperMaster/internal/config"

type toolBudget string

const (
	budgetRead   toolBudget = "read"
	budgetWrite  toolBudget = "write"
	budgetHeavy  toolBudget = "heavy"
	budgetImport toolBudget = "import"
)

// Content size or rendering cost overrides the manifest's read-only hint.
// Other tools inherit read/write classification automatically at registration.
var toolBudgetOverrides = map[string]toolBudget{
	"papers_manager_document_read_markdown":    budgetHeavy,
	"papers_manager_reading_note_export":       budgetHeavy,
	"papers_manager_document_read_page_image":  budgetHeavy,
	"papers_manager_document_read_block_image": budgetHeavy,
	"papers_manager_library_import_file":       budgetImport,
	"papers_manager_library_import_files":      budgetImport,
}

func newToolBudgets(cfg config.Config) map[toolBudget]chan struct{} {
	// Constructors also accept a zero-value Config in standalone adapters.
	makeSlots := func(value, fallback int) chan struct{} {
		if value <= 0 {
			value = fallback
		}
		return make(chan struct{}, value)
	}
	return map[toolBudget]chan struct{}{
		budgetRead:   makeSlots(cfg.MCPReadConcurrency, 128),
		budgetWrite:  makeSlots(cfg.MCPWriteConcurrency, 32),
		budgetHeavy:  makeSlots(cfg.MCPHeavyConcurrency, 4),
		budgetImport: makeSlots(cfg.MCPImportConcurrency, 2),
	}
}

func assignToolBudgets(items []manifestTool, registry map[string]registeredTool, budgets map[toolBudget]chan struct{}) {
	for _, item := range items {
		group := budgetWrite
		if item.Annotations.ReadOnlyHint {
			group = budgetRead
		}
		if override, ok := toolBudgetOverrides[item.Name]; ok {
			group = override
		}
		tool := registry[item.Name]
		tool.budget, tool.slots = group, budgets[group]
		registry[item.Name] = tool
	}
}
