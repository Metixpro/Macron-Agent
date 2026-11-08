package tools

// What a tool says about the files it made: they stay in the workspace, and
// the model decides what the user gets (send_file, or ![caption](file) in its
// answer). Nothing is sent by itself.
const (
	SendNote       = "It reaches the user only if you send it (![caption](file) in your answer, or send_file)"
	SendNotePlural = "they reach the user only if you send them (![caption](file) in your answer, or send_file)"
)
