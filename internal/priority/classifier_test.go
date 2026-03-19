package priority

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		headerValue string
		expected    string
	}{
		{
			name:        "Explicit HIGH header wins over normal path",
			path:        "/normal/path",
			headerValue: "HIGH",
			expected:    "HIGH",
		},
		{
			name:        "Explicit LOW header wins over high-priority path",
			path:        "/api/critical",
			headerValue: "LOW",
			expected:    "LOW",
		},
		{
			name:        "Empty header falls back to high priority path",
			path:        "/api/payment/checkout",
			headerValue: "",
			expected:    "HIGH",
		},
		{
			name:        "Invalid header falls back to high priority path",
			path:        "/admin/settings",
			headerValue: "INVALID_HEADER",
			expected:    "HIGH",
		},
		{
			name:        "Empty header falls back to default path",
			path:        "/user/profile",
			headerValue: "",
			expected:    "LOW",
		},
		{
			name:        "Exact match for high priority path",
			path:        "/health-check",
			headerValue: "",
			expected:    "HIGH",
		},
		{
			name:        "No header, unrecognised path",
			path:        "/",
			headerValue: "",
			expected:    "LOW",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := Classify(tc.path, tc.headerValue)
			if result != tc.expected {
				t.Errorf("Expected %s, got %s for path=%q and header=%q", tc.expected, result, tc.path, tc.headerValue)
			}
		})
	}
}
