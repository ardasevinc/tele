package output

import (
	"encoding/json"
	"fmt"
	"io"
)

type Format string

const (
	Human Format = "human"
	JSON  Format = "json"
	JSONL Format = "jsonl"
)

type Writer struct {
	Out    io.Writer
	Err    io.Writer
	Format Format
	Quiet  bool
}

func (w Writer) Print(v any) error {
	if w.Format == JSON {
		enc := json.NewEncoder(w.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	_, err := fmt.Fprintln(w.Out, v)
	return err
}

func (w Writer) JSON(v any) error {
	enc := json.NewEncoder(w.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (w Writer) JSONL(items []any) error {
	enc := json.NewEncoder(w.Out)
	for _, item := range items {
		if err := enc.Encode(item); err != nil {
			return err
		}
	}
	return nil
}

func (w Writer) Info(format string, args ...any) {
	if w.Quiet || w.Format != Human {
		return
	}
	_, _ = fmt.Fprintf(w.Err, "info: "+format+"\n", args...)
}

func (w Writer) Warn(format string, args ...any) {
	if w.Quiet || w.Format != Human {
		return
	}
	_, _ = fmt.Fprintf(w.Err, "warn: "+format+"\n", args...)
}

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
