package apperror

import (
	"fmt"
	"strings"
)

type ErrCode int

const (
	_ ErrCode = iota
	ClientCodeInvalidArgument
	ClientCodeStorageConflict
	CodeInternalServerError
	CodeUnknownError
)

type Operation string

type FieldError struct {
	Field       string
	Description string
}

func (f FieldError) getMessage() string {
	return fmt.Sprintf("Error in field: %s,description: %s", f.Field, f.Description)
}

type DetailedError interface {
	GiveErr() AppError
	error
}

type AppError struct {
	Op                Operation
	Code              ErrCode
	Message           string
	Fields            []FieldError
	Err               error
	replace           bool
	merge             bool
	priorityErrSearch bool
}

func (ap AppError) GiveErr() AppError {
	return ap
}

func (apperr AppError) Error() string {
	return apperr.Err.Error()
}

func (apperr AppError) Unwrap() error {
	return apperr.Err
}

func (apperr AppError) GetMergeStatus() bool {
	return apperr.merge
}

func (apperr AppError) Merge() AppError {
	apperr.merge = true
	apperr.replace = false
	return apperr
}

func (apperr AppError) SetPriorityErr() AppError {
	apperr.priorityErrSearch = true
	return apperr
}

func (apperr AppError) RemovePriorityErr() AppError {
	apperr.priorityErrSearch = false
	return apperr
}

func (apperr AppError) GetPriorityErr() bool {
	return apperr.priorityErrSearch
}

func (apperr AppError) UnMerge() AppError {
	apperr.merge = false
	apperr.replace = true
	return apperr
}

type ErrDetails struct {
	Fields  []FieldError
	Message string
}

func InvalidArgumentError(op Operation, errDetails ErrDetails, err error) AppError {
	errorSlice := make([]string, 0, len(errDetails.Fields))
	for _, f := range errDetails.Fields {
		errorSlice = append(errorSlice, f.getMessage())
	}
	return AppError{
		Op:      op,
		Code:    ClientCodeInvalidArgument,
		Fields:  errDetails.Fields,
		Err:     fmt.Errorf("invalid arguments are: %s, error is: %w", strings.Join(errorSlice, "\n"), err),
		Message: errDetails.Message,
	}
}

func PreconditionFailedError(op Operation, errDetails ErrDetails, err error) AppError {
	errorSlice := make([]string, 0, len(errDetails.Fields))
	for _, f := range errDetails.Fields {
		errorSlice = append(errorSlice, f.getMessage())
	}
	return AppError{
		Op:      op,
		Code:    ClientCodeStorageConflict,
		Message: errDetails.Message,
		Fields:  errDetails.Fields,
		Err:     fmt.Errorf("failed preconditions are: %s, error is: %w", strings.Join(errorSlice, "\n"), err),
	}
}

func InternalServerError(op Operation, err error, message string) AppError {
	return AppError{
		Op:  op,
		Err: fmt.Errorf("%s:%w,", message, err),
	}
}

func CatchAllUnknownServerError(op Operation, err error, msg string) AppError {
	return AppError{
		Op:   op,
		Err:  err,
		Code: CodeUnknownError,
	}
}
