# HTTP Log Exporter

A Prometheus exporter that scrapes container logs for HTTP 4xx and 5xx errors, providing detailed metrics for monitoring web application health and error rates.

## Features

- **Multi-format log parsing**: Supports Apache Combined Log Format, Common Log Format, JSON logs, and fallback pattern matching
- **Kubernetes integration**: Automatically discovers and scrapes logs from pods in specified namespaces
- **Flexible filtering**: Support for pod label selectors and namespace targeting
- **Rich metrics**: Provides both error counts and total request metrics with detailed labels
- **Health monitoring**: Includes scrape status and error tracking metrics
- **Configurable**: Environment variable-based configuration for easy deployment

## Metrics Exposed

### `http_errors_total`
Counter tracking HTTP errors (4xx and 5xx status codes) scraped from container logs.

**Labels:**
- `namespace`: Kubernetes namespace
- `pod`: Pod name  
- `container`: Container name
- `status_code`: HTTP status code (e.g., "404", "500")
- `error_class`: Error class ("4xx" or "5xx")

### `http_requests_total`
Counter tracking all HTTP requests with status codes scraped from container logs.

**Labels:**
- `namespace`: Kubernetes namespace
- `pod`: Pod name
- `container`: Container name  
- `status_code`: HTTP status code

### `http_log_scraper_last_scrape_timestamp_seconds`
Gauge showing the Unix timestamp of the last successful log scrape per container.

**Labels:**
- `namespace`: Kubernetes namespace
- `pod`: Pod name
- `container`: Container name

### `http_log_scraper_errors_total`
Counter tracking errors encountered during log scraping.

**Labels:**
- `namespace`: Kubernetes namespace
- `pod`: Pod name
- `container`: Container name
- `error_type`: Type of error (e.g., "scrape_failed")

## Configuration

All configuration is done via environment variables:

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `TARGET_NAMESPACE` | `default` | Kubernetes namespace to scrape logs from |
| `SCRAPE_INTERVAL_SECONDS` | `30` | Interval between log scrapes in seconds |
| `LOG_LINES_LIMIT` | `100` | Maximum number of log lines to fetch per container |
| `POD_SELECTOR` | (empty) | Kubernetes label selector for filtering pods |
| `PORT` | `8080` | HTTP server port for metrics endpoint |

## Supported Log Formats

The exporter can parse multiple log formats:

### Apache Combined Log Format
```
127.0.0.1 - - [25/Dec/2019:01:17:21 +0000] "GET /api/health HTTP/1.1" 200 612
```

### Apache Common Log Format  
```
172.16.0.1 - - [20/Feb/2024:14:15:30 +0000] "PUT /api/data HTTP/1.1" 422 256
```

### JSON Log Format
```json
{"timestamp":"2024-01-01T12:00:00Z","level":"error","status":500,"message":"Internal error"}
```

### Fallback Pattern Matching
For logs that don't match standard formats, the exporter will attempt to extract HTTP status codes using pattern matching:
```
ERROR: Request failed with status 404 - Not Found
Service unavailable: returning 503 status
```

## Usage

### Local Development

1. **Set up environment**:
```bash
export TARGET_NAMESPACE=my-app-namespace
export SCRAPE_INTERVAL_SECONDS=60
export LOG_LINES_LIMIT=200
export POD_SELECTOR="app=web-server"
```

2. **Build and run**:
```bash
cd exporters/httplogexporter
go build -o httplogexporter .
./httplogexporter
```

3. **Access metrics**:
```bash
curl http://localhost:8080/metrics
curl http://localhost:8080/health
```

### Docker Deployment

```dockerfile
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY exporters/httplogexporter/ ./
RUN go build -o httplogexporter .

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/httplogexporter .
EXPOSE 8080
CMD ["./httplogexporter"]
```

### Kubernetes Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: http-log-exporter
  namespace: monitoring
spec:
  replicas: 1
  selector:
    matchLabels:
      app: http-log-exporter
  template:
    metadata:
      labels:
        app: http-log-exporter
    spec:
      serviceAccountName: http-log-exporter
      containers:
      - name: exporter
        image: your-registry/http-log-exporter:latest
        ports:
        - containerPort: 8080
          name: metrics
        env:
        - name: TARGET_NAMESPACE
          value: "production"
        - name: SCRAPE_INTERVAL_SECONDS
          value: "30"
        - name: LOG_LINES_LIMIT
          value: "500"
        - name: POD_SELECTOR
          value: "tier=frontend"
        resources:
          requests:
            memory: "64Mi"
            cpu: "50m"
          limits:
            memory: "128Mi"
            cpu: "100m"
        livenessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 30
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 5
---
apiVersion: v1
kind: Service
metadata:
  name: http-log-exporter-service
  namespace: monitoring
  labels:
    app: http-log-exporter
spec:
  ports:
  - port: 8080
    targetPort: 8080
    name: metrics
  selector:
    app: http-log-exporter
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: http-log-exporter
  namespace: monitoring
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: http-log-exporter
rules:
- apiGroups: [""]
  resources: ["pods", "pods/log"]
  verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: http-log-exporter
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: http-log-exporter
subjects:
- kind: ServiceAccount
  name: http-log-exporter
  namespace: monitoring
```

### Prometheus Configuration

Add the following to your Prometheus configuration:

```yaml
scrape_configs:
- job_name: 'http-log-exporter'
  static_configs:
  - targets: ['http-log-exporter-service:8080']
  scrape_interval: 30s
  metrics_path: /metrics
```

## Sample Queries

### Error Rate by Pod
```promql
rate(http_errors_total[5m])
```

### 4xx vs 5xx Error Comparison
```promql
sum(rate(http_errors_total{error_class="4xx"}[5m])) by (namespace, pod)
vs
sum(rate(http_errors_total{error_class="5xx"}[5m])) by (namespace, pod)
```

### Request Success Rate
```promql
(
  sum(rate(http_requests_total[5m])) by (namespace, pod) -
  sum(rate(http_errors_total[5m])) by (namespace, pod)
) / sum(rate(http_requests_total[5m])) by (namespace, pod) * 100
```

### Top Error Status Codes
```promql
topk(10, sum(rate(http_errors_total[1h])) by (status_code))
```

## Troubleshooting

### Common Issues

1. **No metrics appearing**:
   - Check RBAC permissions for pod access
   - Verify target namespace exists and contains running pods
   - Ensure pods are generating HTTP logs in supported formats

2. **Permission denied errors**:
   - Verify the ServiceAccount has proper ClusterRole permissions
   - Check that the exporter can access the Kubernetes API

3. **High memory usage**:
   - Reduce `LOG_LINES_LIMIT` to process fewer lines per scrape
   - Increase `SCRAPE_INTERVAL_SECONDS` to scrape less frequently
   - Use more specific `POD_SELECTOR` to target fewer pods

### Debug Mode

Set log level to debug for troubleshooting:
```bash
export LOG_LEVEL=debug
```

### Testing Log Parsing

To test if your log format is supported:
```bash
# Check if the regex patterns match your log format
go test -v ./... -run TestParseLogLine
```

## Contributing

1. Fork the repository
2. Create a feature branch
3. Add tests for any new functionality
4. Run tests: `go test -v ./...`
5. Submit a pull request

## License

This project is licensed under the same license as the parent repository. 