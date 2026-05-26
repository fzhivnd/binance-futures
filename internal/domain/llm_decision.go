package domain

type LLMDecision struct {
	Action       string
	Symbol       string
	Confidence   int
	EntryMode    EntryMode
	TPStrategy   string
	EntryReasons []string
	Warnings     []string
	SkipReason   string
}
