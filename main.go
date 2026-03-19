package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

var cfg struct {
	Domain    string
	AuthToken string
	CertDir   string
}

func loadEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			os.Setenv(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	loadEnv("/root/workspace/.env")

	cfg.Domain = envOr("WS_DOMAIN", "hcwkapi.bygsoga.cc")
	cfg.AuthToken = envOr("WS_AUTH_TOKEN", "capy-workspace-7f3a9b2e")
	cfg.CertDir = envOr("WS_CERT_DIR", "/root/workspace/.certs")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/read", withAuth(handleRead))
	mux.HandleFunc("/api/write", withAuth(handleWrite))
	mux.HandleFunc("/api/edit", withAuth(handleEdit))
	mux.HandleFunc("/api/exec", withAuth(handleExec))
	mux.HandleFunc("/api/glob", withAuth(handleGlob))
	mux.HandleFunc("/api/grep", withAuth(handleGrep))
	mux.HandleFunc("/api/edit-lines", withAuth(handleEditLines))
	mux.HandleFunc("/api/patch", withAuth(handlePatch))
	mux.HandleFunc("/api/ping", handlePing)

	certManager := autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(cfg.Domain),
		Cache:      autocert.DirCache(cfg.CertDir),
	}

	tlsSrv := &http.Server{
		Addr:    ":443",
		Handler: mux,
		TLSConfig: &tls.Config{
			GetCertificate: certManager.GetCertificate,
			MinVersion:     tls.VersionTLS12,
		},
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	httpSrv := &http.Server{
		Addr:         ":80",
		Handler:      certManager.HTTPHandler(http.HandlerFunc(redirectHTTPS)),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("[workspace-api] HTTP :80 (ACME + redirect)")
		if err := httpSrv.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("HTTP error: %v", err)
		}
	}()

	go func() {
		log.Printf("[workspace-api] HTTPS :443 (domain: %s)", cfg.Domain)
		if err := tlsSrv.ListenAndServeTLS("", ""); err != http.ErrServerClosed {
			log.Fatalf("HTTPS error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("[workspace-api] Shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tlsSrv.Shutdown(ctx)
	httpSrv.Shutdown(ctx)
}

func redirectHTTPS(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusMovedPermanently)
}

func withAuth(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, 405, "Method not allowed")
			return
		}
		token := r.Header.Get("Authorization")
		if strings.HasPrefix(token, "Bearer ") {
			token = token[7:]
		}
		if token != cfg.AuthToken {
			jsonError(w, 401, "Unauthorized")
			return
		}
		handler(w, r)
	}
}

func handlePing(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]interface{}{"pong": true, "time": time.Now().Unix()})
}

func handleRead(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string `json:"path"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
		Numbered bool   `json:"numbered"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, 400, "Invalid JSON: "+err.Error())
		return
	}
	if req.Path == "" {
		jsonError(w, 400, "path is required")
		return
	}

	data, err := os.ReadFile(req.Path)
	if err != nil {
		jsonError(w, 404, "Read failed: "+err.Error())
		return
	}

	content := string(data)
	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	if req.Offset > 0 && req.Offset < len(lines) {
		lines = lines[req.Offset:]
	}
	if req.Limit > 0 && req.Limit < len(lines) {
		lines = lines[:req.Limit]
	}

	startLine := req.Offset + 1
	if req.Offset <= 0 {
		startLine = 1
	}

	output := strings.Join(lines, "\n")
	if req.Numbered {
		var numbered []string
		for i, line := range lines {
			numbered = append(numbered, fmt.Sprintf("%4d | %s", startLine+i, line))
		}
		output = strings.Join(numbered, "\n")
	}

	jsonOK(w, map[string]interface{}{
		"content":     output,
		"total_lines": totalLines,
		"size":        len(data),
		"start_line":  startLine,
	})
}

func handleWrite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, 400, "Invalid JSON: "+err.Error())
		return
	}
	if req.Path == "" {
		jsonError(w, 400, "path is required")
		return
	}

	dir := filepath.Dir(req.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		jsonError(w, 500, "Failed to create directory: "+err.Error())
		return
	}

	if err := os.WriteFile(req.Path, []byte(req.Content), 0644); err != nil {
		jsonError(w, 500, "Write failed: "+err.Error())
		return
	}

	jsonOK(w, map[string]interface{}{"written": len(req.Content)})
}

func handleEdit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path       string `json:"path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, 400, "Invalid JSON: "+err.Error())
		return
	}
	if req.Path == "" || req.OldString == "" {
		jsonError(w, 400, "path and old_string are required")
		return
	}

	data, err := os.ReadFile(req.Path)
	if err != nil {
		jsonError(w, 404, "Read failed: "+err.Error())
		return
	}

	content := string(data)
	count := strings.Count(content, req.OldString)

	if count == 0 {
		jsonError(w, 400, "old_string not found in file")
		return
	}
	if !req.ReplaceAll && count > 1 {
		jsonError(w, 400, fmt.Sprintf("old_string found %d times, not unique. Use replace_all=true or provide more context", count))
		return
	}

	var newContent string
	if req.ReplaceAll {
		newContent = strings.ReplaceAll(content, req.OldString, req.NewString)
	} else {
		newContent = strings.Replace(content, req.OldString, req.NewString, 1)
	}

	if err := os.WriteFile(req.Path, []byte(newContent), 0644); err != nil {
		jsonError(w, 500, "Write failed: "+err.Error())
		return
	}

	jsonOK(w, map[string]interface{}{"replacements": count})
}

func handleEditLines(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Action  string `json:"action"`
		Start   int    `json:"start"`
		End     int    `json:"end"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, 400, "Invalid JSON: "+err.Error())
		return
	}
	if req.Path == "" {
		jsonError(w, 400, "path is required")
		return
	}
	if req.Action == "" {
		jsonError(w, 400, "action is required (delete, insert, replace)")
		return
	}
	if req.Start < 1 {
		jsonError(w, 400, "start must be >= 1")
		return
	}
	if req.End < 1 {
		req.End = req.Start
	}

	data, err := os.ReadFile(req.Path)
	if err != nil {
		jsonError(w, 404, "Read failed: "+err.Error())
		return
	}

	lines := strings.Split(string(data), "\n")
	totalLines := len(lines)

	if req.Start > totalLines {
		jsonError(w, 400, fmt.Sprintf("start %d exceeds total lines %d", req.Start, totalLines))
		return
	}
	if req.End > totalLines {
		req.End = totalLines
	}

	var result []string
	var info string

	switch req.Action {
	case "delete":
		result = append(result, lines[:req.Start-1]...)
		result = append(result, lines[req.End:]...)
		info = fmt.Sprintf("deleted lines %d-%d (%d lines)", req.Start, req.End, req.End-req.Start+1)
	case "insert":
		newLines := strings.Split(req.Content, "\n")
		result = append(result, lines[:req.Start-1]...)
		result = append(result, newLines...)
		result = append(result, lines[req.Start-1:]...)
		info = fmt.Sprintf("inserted %d lines at line %d", len(newLines), req.Start)
	case "replace":
		newLines := strings.Split(req.Content, "\n")
		result = append(result, lines[:req.Start-1]...)
		result = append(result, newLines...)
		result = append(result, lines[req.End:]...)
		info = fmt.Sprintf("replaced lines %d-%d with %d lines", req.Start, req.End, len(newLines))
	default:
		jsonError(w, 400, "action must be delete, insert, or replace")
		return
	}

	if err := os.WriteFile(req.Path, []byte(strings.Join(result, "\n")), 0644); err != nil {
		jsonError(w, 500, "Write failed: "+err.Error())
		return
	}

	jsonOK(w, map[string]interface{}{
		"info":        info,
		"old_lines":   totalLines,
		"new_lines":   len(result),
	})
}

func handlePatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path  string `json:"path"`
		Edits []struct {
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		} `json:"edits"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, 400, "Invalid JSON: "+err.Error())
		return
	}
	if req.Path == "" {
		jsonError(w, 400, "path is required")
		return
	}
	if len(req.Edits) == 0 {
		jsonError(w, 400, "edits array is required")
		return
	}

	data, err := os.ReadFile(req.Path)
	if err != nil {
		jsonError(w, 404, "Read failed: "+err.Error())
		return
	}

	content := string(data)
	applied := 0

	for i, edit := range req.Edits {
		if edit.OldString == "" {
			jsonError(w, 400, fmt.Sprintf("edit %d: old_string is empty", i))
			return
		}
		count := strings.Count(content, edit.OldString)
		if count == 0 {
			jsonError(w, 400, fmt.Sprintf("edit %d: old_string not found", i))
			return
		}
		if count > 1 {
			jsonError(w, 400, fmt.Sprintf("edit %d: old_string found %d times, not unique", i, count))
			return
		}
		content = strings.Replace(content, edit.OldString, edit.NewString, 1)
		applied++
	}

	if err := os.WriteFile(req.Path, []byte(content), 0644); err != nil {
		jsonError(w, 500, "Write failed: "+err.Error())
		return
	}

	jsonOK(w, map[string]interface{}{"edits_applied": applied})
}

func handleExec(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Command string `json:"command"`
		Dir     string `json:"dir"`
		Timeout int    `json:"timeout"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, 400, "Invalid JSON: "+err.Error())
		return
	}
	if req.Command == "" {
		jsonError(w, 400, "command is required")
		return
	}
	if req.Timeout <= 0 {
		req.Timeout = 60
	}
	if req.Timeout > 300 {
		req.Timeout = 300
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(req.Timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", req.Command)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	cmd.Env = append(os.Environ(),
		"PATH=/root/go/bin:/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"GOPATH=/root/go",
	)

	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	exitCode := 0
	timedOut := false
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			timedOut = true
			exitCode = 124
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			jsonError(w, 500, "Exec failed: "+err.Error())
			return
		}
	}

	// Combined output for backward compat, plus separate stdout/stderr
	combined := stdoutBuf.String() + stderrBuf.String()
	jsonOK(w, map[string]interface{}{
		"output":    combined,
		"stdout":    stdoutBuf.String(),
		"stderr":    stderrBuf.String(),
		"exit_code": exitCode,
		"timed_out": timedOut,
	})
}

func handleGlob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, 400, "Invalid JSON: "+err.Error())
		return
	}
	if req.Pattern == "" {
		jsonError(w, 400, "pattern is required")
		return
	}
	if req.Path == "" {
		req.Path = "/root/workspace"
	}

	var matches []string
	filepath.WalkDir(req.Path, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		matched, _ := filepath.Match(req.Pattern, filepath.Base(path))
		if matched {
			matches = append(matches, path)
		}
		return nil
	})
	if matches == nil {
		matches = []string{}
	}

	jsonOK(w, map[string]interface{}{"files": matches, "count": len(matches)})
}

func handleGrep(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Glob    string `json:"glob"`
		Context int    `json:"context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, 400, "Invalid JSON: "+err.Error())
		return
	}
	if req.Pattern == "" {
		jsonError(w, 400, "pattern is required")
		return
	}
	if req.Path == "" {
		req.Path = "/root/workspace"
	}

	args := []string{"-rn"}
	if req.Context > 0 {
		args = append(args, fmt.Sprintf("-C%d", req.Context))
	}
	if req.Glob != "" {
		args = append(args, "--include="+req.Glob)
	}
	args = append(args, req.Pattern, req.Path)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "grep", args...)
	output, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	jsonOK(w, map[string]interface{}{
		"output":    string(output),
		"exit_code": exitCode,
	})
}

func jsonOK(w http.ResponseWriter, data map[string]interface{}) {
	data["ok"] = true
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":    false,
		"error": message,
	})
}
