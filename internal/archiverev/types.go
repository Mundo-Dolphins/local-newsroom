package archiverev

// BudgetChecker tracks context and source budget usage.
type BudgetChecker struct {
	maxSourceCount int
	maxContextSize int

	sourceCount int
	contextSize int
}

// NewBudgetChecker creates a budget checker with the given limits.
func NewBudgetChecker(maxSourceCount, maxContextSize int) *BudgetChecker {
	return &BudgetChecker{
		maxSourceCount: maxSourceCount,
		maxContextSize: maxContextSize,
	}
}

// CanAddSource reports whether we can add another source.
func (b *BudgetChecker) CanAddSource() bool {
	return b.sourceCount < b.maxSourceCount
}

// CanAddContext reports whether we can add more context.
func (b *BudgetChecker) CanAddContext() bool {
	return b.contextSize < b.maxContextSize
}

// AddSource increments the source count.
func (b *BudgetChecker) AddSource() {
	b.sourceCount++
}

// AddContext adds the given number of characters to the context.
func (b *BudgetChecker) AddContext(chars int) {
	b.contextSize += chars
}

// SourceCount returns the current source count.
func (b *BudgetChecker) SourceCount() int {
	return b.sourceCount
}

// ContextSize returns the current context size.
func (b *BudgetChecker) ContextSize() int {
	return b.contextSize
}
