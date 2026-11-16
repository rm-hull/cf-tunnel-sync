package web

import (
	"fmt"
	"html/template"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// cspMiddleware adds security headers. The policy is relaxed to allow
// Chrome DevTools to work (default-src 'self' instead of 'none').
func cspMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Security-Policy", "default-src 'self'; style-src 'self'; img-src 'self' data:; base-uri 'self'; frame-ancestors 'none'")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Next()
	}
}

// handleIndex renders the HTML status page.
func (s *Server) handleIndex(c *gin.Context) {
	data := s.buildStatusData()

	funcMap := template.FuncMap{
		"healthySentence": healthySentence,
		"formatDuration":  formatDuration,
		"formatUptime":    formatUptime,
		"formatTime":      formatTime,
		"formatAttrs":     formatAttrs,
		"statusClass":     statusClass,
		"statusLabel":     statusLabel,
		"last": func(n int, items []LogEvent) []LogEvent {
			if n >= len(items) {
				return items
			}
			return items[len(items)-n:]
		},
	}

	tmpl, err := template.New("status.html").Funcs(funcMap).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		c.String(http.StatusInternalServerError, "template error: %v", err)
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if s.statusRefresh > 0 {
		c.Header("Refresh", strings.TrimSuffix(s.statusRefresh.String(), "s"))
	}

	if err := tmpl.Execute(c.Writer, data); err != nil {
		c.String(http.StatusInternalServerError, "template error: %v", err)
		return
	}
}

// handleStatusJSON returns the status as JSON.
func (s *Server) handleStatusJSON(c *gin.Context) {
	data := s.buildStatusData()
	c.JSON(http.StatusOK, data)
}

// handleHealthz returns 200 while the process is up.
func (s *Server) handleHealthz(c *gin.Context) {
	c.String(http.StatusOK, "ok")
}

// handleReadyz returns 200 only if the daemon is ready.
func (s *Server) handleReadyz(c *gin.Context) {
	snap := s.daemon.Snapshot()

	// Must have completed at least one pass
	if snap.PassCount < 1 {
		c.String(http.StatusServiceUnavailable, "not ready: no reconcile completed")
		return
	}

	// Last pass must be recent (< 3x resync_interval)
	if snap.LastDuration > 0 && snap.PassCount > 0 {
		// We don't have lastAt in the snapshot yet, so we check if there was a recent error
		// For now, if we have passes and no error, we're ready
		if snap.LastError != nil {
			c.String(http.StatusServiceUnavailable, "not ready: %v", snap.LastError)
			return
		}
	}

	c.String(http.StatusOK, "ready")
}

// handleMetrics exposes Prometheus metrics including daemon stats.
func (s *Server) handleMetrics(c *gin.Context) {
	gatherer := s.metricsGatherer
	if gatherer == nil {
		gatherer = prometheus.DefaultGatherer
	}
	handler := promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{})
	handler.ServeHTTP(c.Writer, c.Request)
}

// handleCSS serves the embedded CSS with immutable cache headers.
func (s *Server) handleCSS(c *gin.Context) {
	c.Header("Content-Type", "text/css; charset=utf-8")
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.String(http.StatusOK, cssContent)
}

// formatDuration formats a duration for display.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return d.String()
	}
	if d < time.Minute {
		return d.Truncate(time.Millisecond).String()
	}
	if d < time.Hour {
		return d.Truncate(time.Second).String()
	}
	return d.Truncate(time.Minute).String()
}

// formatUptime formats uptime seconds for display.
func formatUptime(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	return formatDuration(d)
}

// formatTime formats a time value truncated to milliseconds.
func formatTime(t time.Time) string {
	t = t.Truncate(time.Millisecond)
	return t.Format("2006-01-02 15:04:05.000 MST")
}

// statusClass returns the CSS class for a rule status.
func statusClass(status string) string {
	switch status {
	case "active":
		return "ok"
	case "pending":
		return "warn"
	case "dns_conflict":
		return "bad"
	default:
		return ""
	}
}

// statusLabel returns a human-readable label for a rule status.
func statusLabel(status string) string {
	switch status {
	case "active":
		return "Active"
	case "pending":
		return "Pending removal"
	case "dns_conflict":
		return "DNS conflict"
	default:
		return status
	}
}

// flattenAttrs recursively flattens nested maps and slices into dot-prefixed key-value pairs.
func flattenAttrs(prefix string, v any, result map[string]string) {
	switch val := v.(type) {
	case map[string]any:
		for k, v := range val {
			flattenAttrs(prefix+k+".", v, result)
		}
	case []any:
		for i, item := range val {
			flattenAttrs(prefix+fmt.Sprintf("[%d]", i)+".", item, result)
		}
	default:
		if prefix != "" {
			result[strings.TrimSuffix(prefix, ".")] = fmt.Sprintf("%v", v)
		}
	}
}

// formatAttrs renders log event key-value attributes as HTML-safe, color-coded spans.
// Nested maps are flattened with dot-prefixed keys (e.g. request.method=GET).
// Keys are sorted alphabetically for consistent output.
func formatAttrs(attrs map[string]any) template.HTML {
	if len(attrs) == 0 {
		return ""
	}
	flat := make(map[string]string)
	for k, v := range attrs {
		flattenAttrs(k+".", v, flat)
	}
	if len(flat) == 0 {
		return ""
	}
	keys := slices.Sorted(maps.Keys(flat))
	var b strings.Builder
	b.WriteString(" ")
	first := true
	for _, k := range keys {
		if !first {
			b.WriteString(" ")
		}
		first = false
		b.WriteString(`<span class="text-cyan-600 dark:text-cyan-400">`)
		b.WriteString(template.HTMLEscapeString(k))
		b.WriteString(`</span>=<span class="text-purple-600 dark:text-purple-400">`)
		b.WriteString(template.HTMLEscapeString(flat[k]))
		b.WriteString(`</span>`)
	}
	// #nosec G203 -- all dynamic content is escaped via template.HTMLEscapeString
	return template.HTML(b.String())
}
