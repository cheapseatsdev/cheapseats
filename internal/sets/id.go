package sets

// PromptID makes an id for a new prompt from its wording, as migrate-config does, unique against
// taken (which it adds the id to).
func PromptID(text string, taken map[string]bool) string { return promptID(text, taken) }
