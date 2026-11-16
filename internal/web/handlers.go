package web

import (
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// cspMiddleware adds Content-Security-Policy header to every response.
func cspMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		c.Next()
	}
}

// handleIndex renders the HTML status page.
func (s *Server) handleIndex(c *gin.Context) {
	data := s.buildStatusData()

	funcMap := template.FuncMap{
		"healthySentence": healthySentence,
		"formatDuration":  formatDuration,
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

// handleMetrics exposes Prometheus metrics.
func (s *Server) handleMetrics(c *gin.Context) {
	promhttp.Handler().ServeHTTP(c.Writer, c.Request)
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
