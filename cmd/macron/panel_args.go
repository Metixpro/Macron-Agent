package main

func arg(parts []string, i int) string {
	if i >= 0 && i < len(parts) {
		return parts[i]
	}
	return ""
}
