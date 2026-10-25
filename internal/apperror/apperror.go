package apperror

import (
	"fmt"
	"strings"
)

type ErrCode int

const (
	ClientCodeInvalidArgument ErrCode = iota
	ClientCodeStorageConflict
	CodeInternalServerError
)

type Operation string

type FieldError struct {
	Field       string
	Description string
}

func (f FieldError) getMessage() string {
	return fmt.Sprintf("Error in field: %s,description: %s", f.Field, f.Description)
}

type AppError struct {
	Op      Operation
	Code    ErrCode
	Message string
	Fields  []FieldError
	Err     error
}

func (apperr AppError) Error() string {
	return apperr.Err.Error()
}

func (apperr AppError) Unwrap() error {
	return apperr.Err
}

func InvalidArgumentError(op Operation, fields []FieldError, err error) AppError {
	errorSlice := make([]string, 0, len(fields))
	for _, f := range fields {
		errorSlice = append(errorSlice, f.getMessage())
	}
	return AppError{
		Op:     op,
		Code:   ClientCodeInvalidArgument,
		Fields: fields,
		Err:    fmt.Errorf("Invalid Arguments are: %s, error is: %w", strings.Join(errorSlice, "\n"), err),
	}
}

func InternalServerError(op Operation, err error, message string) AppError {
	return AppError{
		Op:  "unknown",
		Err: fmt.Errorf("%s:%w,", message, err),
	}
}

func CatchAllInternalServerError(err error) AppError {
	return AppError{
		Op:  "unknown",
		Err: err,
	}
}
