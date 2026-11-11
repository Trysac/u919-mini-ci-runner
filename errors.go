package minicirunner

import "fmt"

// Error codes returned by Parse and Run.
const (
	ErrCodeParse             = "parse"
	ErrCodeInputTooLarge     = "input_too_large"
	ErrCodeAliasesNotAllowed = "aliases_not_allowed"
	ErrCodeTooDeep           = "too_deep"
	ErrCodeInvalidPath       = "invalid_path"
	ErrCodeCycle             = "cycle"
	ErrCodeUnknownJob        = "unknown_job"
	ErrCodeMatrixTooLarge    = "matrix_too_large"
	ErrCodeInvalidTimeout    = "invalid_timeout"
	ErrCodeRuntime           = "runtime"
)

// Error is the structured error type returned by all operations in this package.
type Error struct {
	Code    string
	Message string
}

// Error implements the standard error interface.
func (e *Error) Error() string {
	return fmt.Sprintf("minicirunner (%s): %s", e.Code, e.Message)
}

func newError(code, msg string) *Error {
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return &Error{Code: code, Message: msg}
}

func newErrorf(code, format string, args ...any) *Error {
	return newError(code, fmt.Sprintf(format, args...))
}
