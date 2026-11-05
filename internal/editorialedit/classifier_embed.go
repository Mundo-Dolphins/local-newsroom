package editorialedit

import _ "embed"

// embeddedClassifierPrompt is the default system prompt for ClassifyLLM.
//
//go:embed prompts/editorial-diff-classifier.prompt
var embeddedClassifierPrompt string
