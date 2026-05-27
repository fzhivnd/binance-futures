package domain

type LLMDecision struct {
	Action       string
	Symbol       string
	Confidence   int
	EntryMode    EntryMode
	EntryReasons []string
	Warnings     []string
	SkipReason   string
}
