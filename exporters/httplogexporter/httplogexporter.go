package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

const (
	// Default configuration values
	defaultNamespace     = "default"
	defaultScrapeInterval = 30 * time.Second
	defaultLogLines      = 100
	
	// Environment variable names for configuration
	namespaceEnvVar      = "TARGET_NAMESPACE"
	scrapeIntervalEnvVar = "SCRAPE_INTERVAL_SECONDS"
	logLinesEnvVar      = "LOG_LINES_LIMIT"
	podSelectorEnvVar   = "POD_SELECTOR"
)

// HTTPLogExporter represents the main exporter structure
type HTTPLogExporter struct {
	clientset       *kubernetes.Clientset
	namespace       string
	scrapeInterval  time.Duration
	logLines        int64
	podSelector     string
	
	// Prometheus metrics
	httpErrorsTotal *prometheus.CounterVec
	httpRequestsTotal *prometheus.CounterVec
	lastScrapeTime  *prometheus.GaugeVec
	scrapeErrors    *prometheus.CounterVec
}

// LogEntry represents a parsed HTTP log entry
type LogEntry struct {
	Timestamp   string
	Method      string
	Path        string 
	StatusCode  int
	ResponseSize int
	UserAgent   string
	PodName     string
	ContainerName string
}

// HTTP log patterns for common log formats
var (
	// Combined log format: 127.0.0.1 - - [25/Dec/2019:01:17:21 +0000] "GET /api/health HTTP/1.1" 200 612
	combinedLogPattern = regexp.MustCompile(`^(\S+) \S+ \S+ \[([^\]]+)\] "(\S+) (\S+) \S+" (\d+) (\d+)`)
	
	// Common log format variations
	commonLogPattern = regexp.MustCompile(`^(\S+) - - \[([^\]]+)\] "(\S+) (\S+) [^"]*" (\d+) (\d+)`)
	
	// JSON log format (extract status from JSON)
	jsonLogPattern = regexp.MustCompile(`"status"\s*:\s*(\d+)`)
	
	// Simple status code extraction
	statusCodePattern = regexp.MustCompile(`\b([45]\d{2})\b`)
)

// NewHTTPLogExporter creates a new instance of the HTTP log exporter
func NewHTTPLogExporter() (*HTTPLogExporter, error) {
	clientset, err := kubernetes.NewForConfig(config.GetConfigOrDie())
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %v", err)
	}

	// Get configuration from environment variables
	namespace := getEnvOrDefault(namespaceEnvVar, defaultNamespace)
	scrapeInterval := parseScrapeInterval()
	logLines := parseLogLines()
	podSelector := os.Getenv(podSelectorEnvVar)

	return &HTTPLogExporter{
		clientset:      clientset,
		namespace:      namespace,
		scrapeInterval: scrapeInterval,
		logLines:       logLines,
		podSelector:    podSelector,
		
		httpErrorsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "http_errors_total",
				Help: "Total number of HTTP errors scraped from container logs",
			},
			[]string{"namespace", "pod", "container", "status_code", "error_class"},
		),
		
		httpRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "http_requests_total", 
				Help: "Total number of HTTP requests scraped from container logs",
			},
			[]string{"namespace", "pod", "container", "status_code"},
		),
		
		lastScrapeTime: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "http_log_scraper_last_scrape_timestamp_seconds",
				Help: "Unix timestamp of the last successful log scrape",
			},
			[]string{"namespace", "pod", "container"},
		),
		
		scrapeErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "http_log_scraper_errors_total",
				Help: "Total number of errors encountered while scraping logs",
			},
			[]string{"namespace", "pod", "container", "error_type"},
		),
	}, nil
}

// getEnvOrDefault returns the environment variable value or default if not set
func getEnvOrDefault(envVar, defaultValue string) string {
	if value := os.Getenv(envVar); value != "" {
		return value
	}
	return defaultValue
}

// parseScrapeInterval parses the scrape interval from environment variable
func parseScrapeInterval() time.Duration {
	if intervalStr := os.Getenv(scrapeIntervalEnvVar); intervalStr != "" {
		if seconds, err := strconv.Atoi(intervalStr); err == nil {
			return time.Duration(seconds) * time.Second
		}
	}
	return defaultScrapeInterval
}

// parseLogLines parses the log lines limit from environment variable
func parseLogLines() int64 {
	if linesStr := os.Getenv(logLinesEnvVar); linesStr != "" {
		if lines, err := strconv.ParseInt(linesStr, 10, 64); err == nil {
			return lines
		}
	}
	return defaultLogLines
}

// Describe implements the prometheus.Collector interface
func (e *HTTPLogExporter) Describe(ch chan<- *prometheus.Desc) {
	e.httpErrorsTotal.Describe(ch)
	e.httpRequestsTotal.Describe(ch)
	e.lastScrapeTime.Describe(ch)
	e.scrapeErrors.Describe(ch)
}

// Collect implements the prometheus.Collector interface
func (e *HTTPLogExporter) Collect(ch chan<- prometheus.Metric) {
	e.httpErrorsTotal.Collect(ch)
	e.httpRequestsTotal.Collect(ch)
	e.lastScrapeTime.Collect(ch)
	e.scrapeErrors.Collect(ch)
}

// scrapeLogs scrapes logs from all pods in the target namespace
func (e *HTTPLogExporter) scrapeLogs(ctx context.Context) error {
	// List pods in the target namespace
	listOptions := metav1.ListOptions{}
	if e.podSelector != "" {
		listOptions.LabelSelector = e.podSelector
	}

	pods, err := e.clientset.CoreV1().Pods(e.namespace).List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("failed to list pods in namespace %s: %v", e.namespace, err)
	}

	log.Printf("Found %d pods in namespace %s", len(pods.Items), e.namespace)

	for _, pod := range pods.Items {
		// Skip pods that are not running
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}

		for _, container := range pod.Spec.Containers {
			if err := e.scrapeContainerLogs(ctx, pod.Name, container.Name); err != nil {
				log.Printf("Error scraping logs from pod %s, container %s: %v", pod.Name, container.Name, err)
				e.scrapeErrors.WithLabelValues(e.namespace, pod.Name, container.Name, "scrape_failed").Inc()
			}
		}
	}

	return nil
}

// scrapeContainerLogs scrapes logs from a specific container
func (e *HTTPLogExporter) scrapeContainerLogs(ctx context.Context, podName, containerName string) error {
	// Get container logs
	req := e.clientset.CoreV1().Pods(e.namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: containerName,
		TailLines: &e.logLines,
		Follow:    false,
	})

	podLogs, err := req.Stream(ctx)
	if err != nil {
		return fmt.Errorf("failed to get logs: %v", err)
	}
	defer podLogs.Close()

	// Parse logs line by line
	scanner := bufio.NewScanner(podLogs)
	lineCount := 0
	
	for scanner.Scan() {
		line := scanner.Text()
		lineCount++
		
		if entry := e.parseLogLine(line, podName, containerName); entry != nil {
			e.updateMetrics(entry)
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		return fmt.Errorf("error reading logs: %v", err)
	}

	// Update last scrape time
	e.lastScrapeTime.WithLabelValues(e.namespace, podName, containerName).SetToCurrentTime()
	
	log.Printf("Processed %d log lines from pod %s, container %s", lineCount, podName, containerName)
	return nil
}

// parseLogLine attempts to parse a log line and extract HTTP information
func (e *HTTPLogExporter) parseLogLine(line, podName, containerName string) *LogEntry {
	// Try different log patterns
	if matches := combinedLogPattern.FindStringSubmatch(line); matches != nil {
		statusCode, _ := strconv.Atoi(matches[5])
		responseSize, _ := strconv.Atoi(matches[6])
		
		return &LogEntry{
			Timestamp:     matches[2],
			Method:        matches[3],
			Path:         matches[4],
			StatusCode:   statusCode,
			ResponseSize: responseSize,
			PodName:      podName,
			ContainerName: containerName,
		}
	}

	if matches := commonLogPattern.FindStringSubmatch(line); matches != nil {
		statusCode, _ := strconv.Atoi(matches[5])
		responseSize, _ := strconv.Atoi(matches[6])
		
		return &LogEntry{
			Timestamp:     matches[2], 
			Method:        matches[3],
			Path:         matches[4], 
			StatusCode:   statusCode,
			ResponseSize: responseSize,
			PodName:      podName,
			ContainerName: containerName,
		}
	}

	// Try JSON log format
	if matches := jsonLogPattern.FindStringSubmatch(line); matches != nil {
		statusCode, _ := strconv.Atoi(matches[1])
		
		return &LogEntry{
			StatusCode:    statusCode,
			PodName:       podName, 
			ContainerName: containerName,
		}
	}

	// Fallback: look for any HTTP status codes
	if matches := statusCodePattern.FindStringSubmatch(line); matches != nil {
		statusCode, _ := strconv.Atoi(matches[1])
		
		return &LogEntry{
			StatusCode:    statusCode,
			PodName:       podName,
			ContainerName: containerName,
		}
	}

	return nil
}

// updateMetrics updates Prometheus metrics based on the parsed log entry
func (e *HTTPLogExporter) updateMetrics(entry *LogEntry) {
	statusCodeStr := strconv.Itoa(entry.StatusCode)
	
	// Update total requests counter
	e.httpRequestsTotal.WithLabelValues(
		e.namespace,
		entry.PodName,
		entry.ContainerName,
		statusCodeStr,
	).Inc()

	// Update error counters for 4xx and 5xx status codes
	if entry.StatusCode >= 400 && entry.StatusCode < 500 {
		e.httpErrorsTotal.WithLabelValues(
			e.namespace,
			entry.PodName,
			entry.ContainerName, 
			statusCodeStr,
			"4xx",
		).Inc()
	} else if entry.StatusCode >= 500 && entry.StatusCode < 600 {
		e.httpErrorsTotal.WithLabelValues(
			e.namespace,
			entry.PodName,
			entry.ContainerName,
			statusCodeStr,
			"5xx", 
		).Inc()
	}
}

// startPeriodicScraping starts the periodic log scraping in a goroutine
func (e *HTTPLogExporter) startPeriodicScraping(ctx context.Context) {
	ticker := time.NewTicker(e.scrapeInterval)
	defer ticker.Stop()

	// Initial scrape
	log.Println("Performing initial log scrape...")
	if err := e.scrapeLogs(ctx); err != nil {
		log.Printf("Initial scrape failed: %v", err)
	}

	log.Printf("Starting periodic log scraping every %v", e.scrapeInterval)
	
	for {
		select {
		case <-ctx.Done():
			log.Println("Stopping periodic scraping")
			return
		case <-ticker.C:
			log.Println("Starting scheduled log scrape...")
			if err := e.scrapeLogs(ctx); err != nil {
				log.Printf("Scheduled scrape failed: %v", err)
			}
		}
	}
}

func main() {
	// Create the exporter
	exporter, err := NewHTTPLogExporter()
	if err != nil {
		log.Fatalf("Failed to create HTTP log exporter: %v", err)
	}

	// Create a new Prometheus registry
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(exporter)

	// Set up HTTP handler for metrics
	http.Handle("/metrics", promhttp.HandlerFor(
		reg,
		promhttp.HandlerOpts{
			EnableOpenMetrics: true,
			Registry:          reg,
		},
	))

	// Health check endpoint
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Start periodic scraping in background
	ctx := context.Background()
	go exporter.startPeriodicScraping(ctx)

	// Start HTTP server
	port := getEnvOrDefault("PORT", "8080")
	log.Printf("HTTP Log Exporter starting on :%s", port)
	log.Printf("Configuration: namespace=%s, scrapeInterval=%v, logLines=%d, podSelector=%s", 
		exporter.namespace, exporter.scrapeInterval, exporter.logLines, exporter.podSelector)
	log.Printf("Metrics available at: http://localhost:%s/metrics", port)
	log.Printf("Health check available at: http://localhost:%s/health", port)

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Failed to start HTTP server: %v", err)
	}
} 