package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func proxyGitHttpBackend(w http.ResponseWriter, r *http.Request, projectRoot string) {
	cmd := exec.Command("git", "http-backend")
	cmd.Env = append(os.Environ(),
		"GIT_PROJECT_ROOT="+projectRoot,
		"GIT_HTTP_EXPORT_ALL=1",
		"PATH_INFO="+r.URL.Path,
		"REQUEST_METHOD="+r.Method,
		"QUERY_STRING="+r.URL.RawQuery,
		"CONTENT_TYPE="+r.Header.Get("Content-Type"),
		"CONTENT_LENGTH="+fmt.Sprintf("%d", r.ContentLength),
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Body != nil {
		_, _ = io.Copy(stdin, r.Body)
	}
	_ = stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	output, err := io.ReadAll(stdout)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := cmd.Wait(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	parts := bytes.SplitN(output, []byte("\r\n\r\n"), 2)
	if len(parts) == 1 {
		parts = bytes.SplitN(output, []byte("\n\n"), 2)
	}
	if len(parts) == 2 {
		var statusCode int = http.StatusOK
		headerBlock := string(parts[0])
		body := parts[1]
		for line := range strings.SplitSeq(headerBlock, "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" {
				continue
			}
			if strings.HasPrefix(strings.ToLower(line), "status:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					if code, err := strconv.Atoi(fields[1]); err == nil {
						statusCode = code
					}
				}
				continue
			}
			key, value, found := strings.Cut(line, ":")
			if !found {
				continue
			}
			w.Header().Set(strings.TrimSpace(key), strings.TrimSpace(value))
		}
		w.WriteHeader(statusCode)
		_, _ = w.Write(body)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(output)
}
