package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// upload is a validated request: the spooled file and its form options.
type upload struct {
	path, model, format string
	stream              bool
}

// readUpload spools the multipart file to a temporary file and validates the
// other fields, rejecting anything unsupported.
func (s *Server) readUpload(w http.ResponseWriter, r *http.Request) (u upload, err error) {
	u.format = "json"
	defer func() {
		if err != nil && u.path != "" {
			os.Remove(u.path)
		}
	}()
	r.Body = http.MaxBytesReader(w, r.Body, s.config.Server.MaxUploadBytes+64*1024)
	mr, err := r.MultipartReader()
	if err != nil {
		return u, invalid("Expected multipart/form-data.", "file")
	}
	seen := map[string]bool{}
	fieldBytes := 0
	for count := 0; ; count++ {
		part, nextErr := mr.NextPart()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return u, uploadError(nextErr)
		}
		if count >= 16 {
			return u, invalid("Too many multipart fields.", "file")
		}
		name := part.FormName()
		if seen[name] {
			return u, invalid("Duplicate multipart field.", name)
		}
		seen[name] = true
		if name == "file" {
			if part.FileName() == "" {
				return u, invalid("file must be an uploaded file.", "file")
			}
			f, createErr := os.CreateTemp(s.config.Server.TempDir, "parakeet-upload-*")
			if createErr != nil {
				return u, createErr
			}
			u.path = f.Name()
			n, copyErr := io.Copy(f, io.LimitReader(part, s.config.Server.MaxUploadBytes+1))
			closeErr := f.Close()
			if copyErr != nil {
				return u, uploadError(copyErr)
			}
			if closeErr != nil {
				return u, closeErr
			}
			if n > s.config.Server.MaxUploadBytes {
				return u, tooLarge()
			}
			if n == 0 {
				return u, invalid("The uploaded file is empty.", "file")
			}
			continue
		}
		if part.FileName() != "" {
			return u, invalid("Only file may contain a file upload.", name)
		}
		switch name {
		case "model", "response_format", "stream":
		default:
			return u, invalid("Unsupported transcription parameter.", name)
		}
		value, readErr := io.ReadAll(io.LimitReader(part, 4097))
		if readErr != nil {
			return u, uploadError(readErr)
		}
		fieldBytes += len(value)
		if len(value) > 4096 || fieldBytes > 16384 {
			return u, invalid("Multipart field is too large.", name)
		}
		switch name {
		case "model":
			u.model = string(value)
		case "response_format":
			u.format = string(value)
		case "stream":
			switch strings.ToLower(string(value)) {
			case "true":
				u.stream = true
			case "false":
			default:
				return u, invalid("stream must be true or false.", name)
			}
		}
	}
	if u.path == "" {
		return u, invalid("file is required.", "file")
	}
	if u.model == "" {
		return u, invalid(fmt.Sprintf("model is required. Set model to %q.", s.config.Model.ID), "model")
	}
	if !s.models[u.model] {
		return u, invalid(fmt.Sprintf("Unknown model. Set model to %q or a configured model alias.", s.config.Model.ID), "model")
	}
	if u.format != "json" && u.format != "text" {
		return u, invalid("Supported response formats are json and text.", "response_format")
	}
	if u.stream && u.format != "json" {
		return u, invalid("Streaming requires response_format=json.", "response_format")
	}
	// A final MIME boundary is not necessarily the end of the HTTP body. Drain
	// the bounded epilogue under the upload deadline, before clearing that
	// deadline or leasing an engine. Otherwise net/http can block while draining
	// a slow epilogue when we eventually write a successful response.
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		return u, uploadError(err)
	}
	return u, nil
}

// uploadError maps a body read error to 413, 408 or 400.
func uploadError(err error) error {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return tooLarge()
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return &apiError{status: 408, Message: "Upload timed out.", Type: "invalid_request_error", Code: "upload_timeout", Param: "file"}
	}
	return invalid("Malformed multipart upload.", "file")
}
