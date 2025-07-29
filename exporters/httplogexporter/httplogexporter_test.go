package main

import (
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
)

func TestParseLogLine(t *testing.T) {
	exporter := &HTTPLogExporter{}
	
	tests := []struct {
		name          string
		line          string
		podName       string
		containerName string
		expected      *LogEntry
		expectNil     bool
	}{
		{
			name:          "Combined log format - 200 OK",
			line:          `127.0.0.1 - - [25/Dec/2019:01:17:21 +0000] "GET /api/health HTTP/1.1" 200 612`,
			podName:       "test-pod",
			containerName: "app",
			expected: &LogEntry{
				Timestamp:     "25/Dec/2019:01:17:21 +0000",
				Method:        "GET",
				Path:          "/api/health",
				StatusCode:    200,
				ResponseSize:  612,
				PodName:       "test-pod",
				ContainerName: "app",
			},
		},
		{
			name:          "Combined log format - 404 Error",
			line:          `192.168.1.100 - - [01/Jan/2024:12:00:00 +0000] "GET /api/notfound HTTP/1.1" 404 0`,
			podName:       "web-pod",
			containerName: "nginx",
			expected: &LogEntry{
				Timestamp:     "01/Jan/2024:12:00:00 +0000",
				Method:        "GET",
				Path:          "/api/notfound",
				StatusCode:    404,
				ResponseSize:  0,
				PodName:       "web-pod",
				ContainerName: "nginx",
			},
		},
		{
			name:          "Combined log format - 500 Error",
			line:          `10.0.0.1 - - [15/Mar/2024:08:30:45 +0000] "POST /api/users HTTP/1.1" 500 1024`,
			podName:       "api-pod",
			containerName: "api",
			expected: &LogEntry{
				Timestamp:     "15/Mar/2024:08:30:45 +0000",
				Method:        "POST",
				Path:          "/api/users",
				StatusCode:    500,
				ResponseSize:  1024,
				PodName:       "api-pod",
				ContainerName: "api",
			},
		},
		{
			name:          "Common log format",
			line:          `172.16.0.1 - - [20/Feb/2024:14:15:30 +0000] "PUT /api/data HTTP/1.1" 422 256`,
			podName:       "data-pod",
			containerName: "service",
			expected: &LogEntry{
				Timestamp:     "20/Feb/2024:14:15:30 +0000",
				Method:        "PUT",
				Path:          "/api/data",
				StatusCode:    422,
				ResponseSize:  256,
				PodName:       "data-pod",
				ContainerName: "service",
			},
		},
		{
			name:          "JSON log format",
			line:          `{"timestamp":"2024-01-01T12:00:00Z","level":"info","msg":"request processed","method":"GET","path":"/health","status":200,"duration":5}`,
			podName:       "json-pod",
			containerName: "app",
			expected: &LogEntry{
				StatusCode:    200,
				PodName:       "json-pod",
				ContainerName: "app",
			},
		},
		{
			name:          "JSON log format - 500 error",
			line:          `{"timestamp":"2024-01-01T12:00:00Z","level":"error","msg":"internal error","status":500}`,
			podName:       "error-pod",
			containerName: "app",
			expected: &LogEntry{
				StatusCode:    500,
				PodName:       "error-pod",
				ContainerName: "app",
			},
		},
		{
			name:          "Status code extraction fallback - 404",
			line:          `ERROR: Request failed with status 404 - Not Found`,
			podName:       "fallback-pod",
			containerName: "app",
			expected: &LogEntry{
				StatusCode:    404,
				PodName:       "fallback-pod",
				ContainerName: "app",
			},
		},
		{
			name:          "Status code extraction fallback - 503",
			line:          `Service unavailable: returning 503 status`,
			podName:       "service-pod",
			containerName: "backend",
			expected: &LogEntry{
				StatusCode:    503,
				PodName:       "service-pod",
				ContainerName: "backend",
			},
		},
		{
			name:          "No status code found",
			line:          `This is just a regular log message without status`,
			podName:       "regular-pod",
			containerName: "app",
			expectNil:     true,
		},
		{
			name:          "Empty line",
			line:          "",
			podName:       "empty-pod",
			containerName: "app",
			expectNil:     true,
		},
		{
			name:          "Non-HTTP status code (not 4xx or 5xx)",
			line:          `Processing item 123 successfully`,
			podName:       "process-pod",
			containerName: "worker",
			expectNil:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := exporter.parseLogLine(tt.line, tt.podName, tt.containerName)
			
			if tt.expectNil {
				assert.Nil(t, result, "Expected nil result but got %+v", result)
				return
			}
			
			assert.NotNil(t, result, "Expected non-nil result")
			assert.Equal(t, tt.expected.StatusCode, result.StatusCode)
			assert.Equal(t, tt.expected.PodName, result.PodName)
			assert.Equal(t, tt.expected.ContainerName, result.ContainerName)
			
			if tt.expected.Timestamp != "" {
				assert.Equal(t, tt.expected.Timestamp, result.Timestamp)
			}
			if tt.expected.Method != "" {
				assert.Equal(t, tt.expected.Method, result.Method)
			}
			if tt.expected.Path != "" {
				assert.Equal(t, tt.expected.Path, result.Path)
			}
			if tt.expected.ResponseSize != 0 {
				assert.Equal(t, tt.expected.ResponseSize, result.ResponseSize)
			}
		})
	}
}

func TestUpdateMetrics(t *testing.T) {
	// Create a test exporter with metrics
	exporter := &HTTPLogExporter{
		namespace: "test-namespace",
		httpErrorsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "http_errors_total",
				Help: "Test metric",
			},
			[]string{"namespace", "pod", "container", "status_code", "error_class"},
		),
		httpRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "http_requests_total",
				Help: "Test metric",
			},
			[]string{"namespace", "pod", "container", "status_code"},
		),
	}

	tests := []struct {
		name               string
		entry              *LogEntry
		expectedRequestInc float64
		expectedErrorInc   float64
		expectedErrorClass string
	}{
		{
			name: "200 OK - no error increment",
			entry: &LogEntry{
				StatusCode:    200,
				PodName:       "test-pod",
				ContainerName: "app",
			},
			expectedRequestInc: 1,
			expectedErrorInc:   0,
		},
		{
			name: "404 Not Found - 4xx error",
			entry: &LogEntry{
				StatusCode:    404,
				PodName:       "test-pod",
				ContainerName: "app",
			},
			expectedRequestInc: 1,
			expectedErrorInc:   1,
			expectedErrorClass: "4xx",
		},
		{
			name: "422 Unprocessable Entity - 4xx error",
			entry: &LogEntry{
				StatusCode:    422,
				PodName:       "api-pod",
				ContainerName: "service",
			},
			expectedRequestInc: 1,
			expectedErrorInc:   1,
			expectedErrorClass: "4xx",
		},
		{
			name: "500 Internal Server Error - 5xx error",
			entry: &LogEntry{
				StatusCode:    500,
				PodName:       "backend-pod",
				ContainerName: "api",
			},
			expectedRequestInc: 1,
			expectedErrorInc:   1,
			expectedErrorClass: "5xx",
		},
		{
			name: "503 Service Unavailable - 5xx error",
			entry: &LogEntry{
				StatusCode:    503,
				PodName:       "service-pod",
				ContainerName: "backend",
			},
			expectedRequestInc: 1,
			expectedErrorInc:   1,
			expectedErrorClass: "5xx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset metrics before test
			exporter.httpRequestsTotal.Reset()
			exporter.httpErrorsTotal.Reset()

			// Update metrics
			exporter.updateMetrics(tt.entry)

			// Check request counter
			requestMetric := exporter.httpRequestsTotal.WithLabelValues(
				exporter.namespace,
				tt.entry.PodName,
				tt.entry.ContainerName,
				strconv.Itoa(tt.entry.StatusCode), // Convert int to string
			)
			
			// Use a different approach to get the metric value
			metric := &dto.Metric{}
			requestMetric.Write(metric)
			assert.Equal(t, tt.expectedRequestInc, metric.GetCounter().GetValue(), 
				"Request counter should increment by %f", tt.expectedRequestInc)

			// Check error counter
			if tt.expectedErrorInc > 0 {
				errorMetric := exporter.httpErrorsTotal.WithLabelValues(
					exporter.namespace,
					tt.entry.PodName,
					tt.entry.ContainerName,
					strconv.Itoa(tt.entry.StatusCode), // Convert int to string
					tt.expectedErrorClass,
				)
				errorMetricData := &dto.Metric{}
				errorMetric.Write(errorMetricData)
				assert.Equal(t, tt.expectedErrorInc, errorMetricData.GetCounter().GetValue(),
					"Error counter should increment by %f", tt.expectedErrorInc)
			}
		})
	}
}

func TestGetEnvOrDefault(t *testing.T) {
	tests := []struct {
		name        string
		envVar      string
		envValue    string
		defaultVal  string
		expected    string
		setEnv      bool
	}{
		{
			name:       "Environment variable set",
			envVar:     "TEST_VAR_SET",
			envValue:   "custom_value",
			defaultVal: "default_value",
			expected:   "custom_value",
			setEnv:     true,
		},
		{
			name:       "Environment variable not set",
			envVar:     "TEST_VAR_NOT_SET",
			defaultVal: "default_value",
			expected:   "default_value",
			setEnv:     false,
		},
		{
			name:       "Environment variable empty",
			envVar:     "TEST_VAR_EMPTY",
			envValue:   "",
			defaultVal: "default_value",
			expected:   "default_value",
			setEnv:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clean up environment variable
			defer func() {
				os.Unsetenv(tt.envVar)
			}()

			if tt.setEnv {
				os.Setenv(tt.envVar, tt.envValue)
			}

			result := getEnvOrDefault(tt.envVar, tt.defaultVal)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseScrapeInterval(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected time.Duration
		setEnv   bool
	}{
		{
			name:     "Valid seconds value",
			envValue: "60",
			expected: 60 * time.Second,
			setEnv:   true,
		},
		{
			name:     "Invalid value",
			envValue: "invalid",
			expected: defaultScrapeInterval,
			setEnv:   true,
		},
		{
			name:     "Environment variable not set",
			expected: defaultScrapeInterval,
			setEnv:   false,
		},
		{
			name:     "Zero value",
			envValue: "0",
			expected: 0 * time.Second,
			setEnv:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clean up environment variable
			defer func() {
				os.Unsetenv(scrapeIntervalEnvVar)
			}()

			if tt.setEnv {
				os.Setenv(scrapeIntervalEnvVar, tt.envValue)
			}

			result := parseScrapeInterval()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseLogLines(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected int64
		setEnv   bool
	}{
		{
			name:     "Valid lines value",
			envValue: "500",
			expected: 500,
			setEnv:   true,
		},
		{
			name:     "Invalid value",
			envValue: "invalid",
			expected: defaultLogLines,
			setEnv:   true,
		},
		{
			name:     "Environment variable not set",
			expected: defaultLogLines,
			setEnv:   false,
		},
		{
			name:     "Zero value",
			envValue: "0",
			expected: 0,
			setEnv:   true,
		},
		{
			name:     "Negative value",
			envValue: "-10",
			expected: -10,
			setEnv:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clean up environment variable
			defer func() {
				os.Unsetenv(logLinesEnvVar)
			}()

			if tt.setEnv {
				os.Setenv(logLinesEnvVar, tt.envValue)
			}

			result := parseLogLines()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestLogPatterns(t *testing.T) {
	tests := []struct {
		name     string
		pattern  *regexp.Regexp
		testLine string
		should   bool
	}{
		// Combined log pattern tests
		{
			name:     "Combined pattern matches valid log",
			pattern:  combinedLogPattern,
			testLine: `127.0.0.1 - - [25/Dec/2019:01:17:21 +0000] "GET /api/health HTTP/1.1" 200 612`,
			should:   true,
		},
		{
			name:     "Combined pattern matches with different IP",
			pattern:  combinedLogPattern,
			testLine: `192.168.1.100 user1 group1 [01/Jan/2024:12:00:00 +0000] "POST /api/data HTTP/1.1" 201 1024`,
			should:   true,
		},
		{
			name:     "Combined pattern doesn't match incomplete log",
			pattern:  combinedLogPattern,
			testLine: `127.0.0.1 - - [25/Dec/2019:01:17:21 +0000] "GET /api/health"`,
			should:   false,
		},
		
		// Common log pattern tests
		{
			name:     "Common pattern matches basic log",
			pattern:  commonLogPattern,
			testLine: `172.16.0.1 - - [20/Feb/2024:14:15:30 +0000] "PUT /api/data HTTP/1.1" 422 256`,
			should:   true,
		},
		{
			name:     "Common pattern doesn't match malformed log",
			pattern:  commonLogPattern,
			testLine: `not a log line at all`,
			should:   false,
		},
		
		// JSON log pattern tests
		{
			name:     "JSON pattern matches status field",
			pattern:  jsonLogPattern,
			testLine: `{"timestamp":"2024-01-01T12:00:00Z","status":200,"message":"OK"}`,
			should:   true,
		},
		{
			name:     "JSON pattern matches status with spaces",
			pattern:  jsonLogPattern,
			testLine: `{"level":"error", "status" : 500, "error":"Internal error"}`,
			should:   true,
		},
		{
			name:     "JSON pattern doesn't match without status",
			pattern:  jsonLogPattern,
			testLine: `{"timestamp":"2024-01-01T12:00:00Z","message":"No status here"}`,
			should:   false,
		},
		
		// Status code pattern tests
		{
			name:     "Status code pattern matches 4xx",
			pattern:  statusCodePattern,
			testLine: `Error 404: Page not found`,
			should:   true,
		},
		{
			name:     "Status code pattern matches 5xx",
			pattern:  statusCodePattern,
			testLine: `Internal server error 500 occurred`,
			should:   true,
		},
		{
			name:     "Status code pattern doesn't match 2xx",
			pattern:  statusCodePattern,
			testLine: `Request successful with status 200`,
			should:   false,
		},
		{
			name:     "Status code pattern doesn't match 3xx",
			pattern:  statusCodePattern,
			testLine: `Redirected with status 301`,
			should:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := tt.pattern.MatchString(tt.testLine)
			if tt.should {
				assert.True(t, matches, "Pattern should match line: %s", tt.testLine)
			} else {
				assert.False(t, matches, "Pattern should not match line: %s", tt.testLine)
			}
		})
	}
}

func TestLogEntryCreation(t *testing.T) {
	entry := &LogEntry{
		Timestamp:     "25/Dec/2019:01:17:21 +0000",
		Method:        "GET",
		Path:          "/api/health",
		StatusCode:    200,
		ResponseSize:  612,
		UserAgent:     "curl/7.64.1",
		PodName:       "test-pod",
		ContainerName: "app-container",
	}

	assert.Equal(t, "25/Dec/2019:01:17:21 +0000", entry.Timestamp)
	assert.Equal(t, "GET", entry.Method)
	assert.Equal(t, "/api/health", entry.Path)
	assert.Equal(t, 200, entry.StatusCode)
	assert.Equal(t, 612, entry.ResponseSize)
	assert.Equal(t, "curl/7.64.1", entry.UserAgent)
	assert.Equal(t, "test-pod", entry.PodName)
	assert.Equal(t, "app-container", entry.ContainerName)
}

// Benchmark tests for performance
func BenchmarkParseLogLine(b *testing.B) {
	exporter := &HTTPLogExporter{}
	testLine := `127.0.0.1 - - [25/Dec/2019:01:17:21 +0000] "GET /api/health HTTP/1.1" 200 612`
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		exporter.parseLogLine(testLine, "test-pod", "app")
	}
}

func BenchmarkMultipleParseLogLine(b *testing.B) {
	exporter := &HTTPLogExporter{}
	testLines := []string{
		`127.0.0.1 - - [25/Dec/2019:01:17:21 +0000] "GET /api/health HTTP/1.1" 200 612`,
		`192.168.1.100 - - [01/Jan/2024:12:00:00 +0000] "POST /api/data HTTP/1.1" 404 0`,
		`{"timestamp":"2024-01-01T12:00:00Z","status":500,"error":"Internal error"}`,
		`Service returned status 422 for invalid input`,
	}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, line := range testLines {
			exporter.parseLogLine(line, "test-pod", "app")
		}
	}
} 