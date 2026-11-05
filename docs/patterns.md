# Patterns Feature

The patterns feature detects repeated editorial feedback patterns in the local newsroom system.

## Overview

The patterns feature identifies recurring correction/edit patterns such as:
- Repeated removal of unsupported superlatives
- Recurring terminology changes
- Repeated preference for shorter intros
- Frequent restructuring of one recipe
- Repeated factual correction on the same entity field

## Usage

### Detect Patterns

Detect patterns in editorial feedback:

```bash
newsroom patterns detect
newsroom patterns detect --min-occurrences 3
newsroom patterns detect --category factual_correction --min-occurrences 2
```

Options:
- `--min-occurrences int` - minimum number of occurrences before a pattern is reported (default 2)
- `--group-by string` - group occurrences by: author, profile, recipe, category (default "author")
- `--time-window string` - time window for occurrences (e.g., 72h, 168h)
- `--category string` - filter by editorial change category
- `--format string` - output format: text or json (default "text")
- `--workspace-root string` - workspace root directory (default ".newsroom")

### List Patterns

List detected patterns:

```bash
newsroom patterns list
newsroom patterns list --category factual_correction
newsroom patterns list --group-by author
```

Options:
- `--format string` - output format: text or json (default "text")
- `--workspace-root string` - workspace root directory (default ".newsroom")

### Show Pattern Details

Show details of a specific pattern:

```bash
newsroom patterns show pattern-abc123
```

Options:
- `--format string` - output format: text or json (default "text")
- `--workspace-root string` - workspace root directory (default ".newsroom")

## Workflow

1. Run `newsroom feedback` to populate feedback records
2. Run `newsroom patterns detect` to detect patterns
3. Review detected patterns
4. Use `newsroom patterns list` to see all patterns
5. Use `newsroom patterns show <id>` to see details

## Pattern Records

Patterns are stored as `KindEditorialPattern` records in the editorial memory. Each pattern record contains:
- Category: The editorial change category
- Group Key: The grouping criteria (author, profile, recipe, or category)
- Minimum Occurrences: The threshold for pattern detection
- Sample Feedback: A representative feedback message
- Occurrences: List of all feedback occurrences
- Notes: Additional notes

## Configuration

The patterns feature uses the editorial memory database. Configure the memory database path in your newsroom configuration:

```yaml
memory:
  enabled: true
  db_path: .newsroom/memory.db
```

## API

The patterns feature is also available as a Go package:

```go
import "github.com/Mundo-Dolphins/local-newsroom/internal/patterns"

// Create a pattern store
store := patterns.NewStore(backing, nil)

// Detect patterns
cfg := patterns.Config{
    MinOccurrences: 2,
    TimeWindow:     72 * time.Hour,
    GroupBy:        patterns.GroupByAuthor,
    Category:       "factual_correction",
}

records, err := store.DetectPatterns(ctx, cfg)
```

See the package documentation for more details.