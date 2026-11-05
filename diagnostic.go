package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Keep project text separate from external data before either interface renders it.
type diagnostic struct {
	human   error
	english string
}

func diagnosticf(english, human string, args ...any) diagnostic {
	original := fmt.Errorf(human, args...)
	translated := append([]any(nil), args...)
	for i, arg := range translated {
		if err, ok := arg.(error); ok {
			translated[i] = errors.New(agentError(err))
		}
	}
	return diagnostic{human: original, english: fmt.Errorf(english, translated...).Error()}
}

func (d diagnostic) Error() string {
	if d.human == nil {
		return ""
	}
	return d.human.Error()
}

func (d diagnostic) Unwrap() error { return d.human }

func (d diagnostic) MarshalJSON() ([]byte, error) { return json.Marshal(d.english) }

func (d *diagnostic) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	d.english, d.human = text, errors.New(text)
	return nil
}

func agentError(err error) string {
	if d, ok := err.(diagnostic); ok {
		return d.english
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var parts []string
		for _, cause := range joined.Unwrap() {
			parts = append(parts, agentError(cause))
		}
		return strings.Join(parts, "\n")
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		return err.Error()
	}
	return fmt.Sprintf("External error: %q", err.Error())
}
