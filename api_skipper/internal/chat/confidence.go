package chat

type Confidence string

const (
	ConfidenceVerified  Confidence = "verified"
	ConfidenceSourced   Confidence = "sourced"
	ConfidenceBestGuess Confidence = "best_guess"
	ConfidenceUnknown   Confidence = "unknown"
)

type SourceType string

const (
	SourceTypeKnowledgeBase SourceType = "knowledge_base"
	SourceTypeWeb           SourceType = "web"
	SourceTypeLLM           SourceType = "llm"
	SourceTypeUnknown       SourceType = "unknown"
)

type Source struct {
	Title string     `json:"title"`
	URL   string     `json:"url"`
	Type  SourceType `json:"type"`
}

type ConfidenceBlock struct {
	Content    string     `json:"content"`
	Confidence Confidence `json:"confidence"`
	Sources    []Source   `json:"sources,omitempty"`
}

func ConfidenceFromSourceType(sourceType SourceType) Confidence {
	switch sourceType {
	case SourceTypeKnowledgeBase:
		return ConfidenceVerified
	case SourceTypeWeb:
		return ConfidenceSourced
	case SourceTypeLLM:
		return ConfidenceBestGuess
	default:
		return ConfidenceUnknown
	}
}
