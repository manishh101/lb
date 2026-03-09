package priority

import "strings"

// highPriorityPaths defines URL path prefixes that auto-classify as HIGH priority.
// These mimic real-world scenarios where certain endpoints are more critical.
var highPriorityPaths = []string{
	"/api/critical",
	"/api/payment",
	"/api/auth",
	"/admin",
	"/health-check",
}

// Classify determines the priority of a request.
// Explicit header takes precedence; URL-based classification is the fallback.
func Classify(path, headerValue string) string {
	// 1. Explicit header always wins
	if headerValue == "HIGH" || headerValue == "LOW" {
		return headerValue
	}
	// 2. URL-based auto-classification
	for _, prefix := range highPriorityPaths {
		if strings.HasPrefix(path, prefix) {
			return "HIGH"
		}
	}
	// 3. Default: LOW priority
	return "LOW"
}
