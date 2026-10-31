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

type detailedError interface {
	GiveErr() AppError
	error
}

type AppError struct {
	Op          Operation
	Code        ErrCode
	Message     string
	Fields      []FieldError
	Err         error
	merge       bool
	isClientErr bool
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

func (apperr AppError) MakeAsClientErr() AppError {
	apperr.isClientErr = true
	return apperr
}

func (apperr AppError) RemoveFromClientErr() AppError {
	apperr.isClientErr = false
	return apperr
}

func (apperr AppError) IsClientErr() bool {
	return apperr.isClientErr
}

func (apperr AppError) MakeClientErrAndMerge() AppError {
	apperr.isClientErr = true
	apperr.merge = true
	return apperr
}

func (apperr AppError) UnMerge() AppError {
	apperr.merge = false
	return apperr
}

func (apperr AppError) GetMergeStatus() bool {
	return apperr.merge
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
		Op:      op,
		Err:     err,
		Code:    CodeUnknownError,
		Message: msg,
	}
}

type errorMessage struct {
	Message string
	Fields  []FieldError
}

func (em errorMessage) accumulate(fields []FieldError, msg string) errorMessage {
	out := errorMessage{Message: em.Message}
	if msg != "" {
		if out.Message != "" {
			out.Message += ";"
		}
		out.Message += msg
	}
	out.Fields = make([]FieldError, 0, len(em.Fields)+len(fields))
	out.Fields = append(out.Fields, em.Fields...)
	out.Fields = append(out.Fields, fields...)
	return out
}

func generateClientFields(err error, acc errorMessage) (errorMessage, detailedError, bool) {
	if err == nil {
		return acc, nil, false
	}

	if de, ok := err.(detailedError); ok {
		appErr := de.GiveErr()
		if appErr.IsClientErr() {
			acc = acc.accumulate(appErr.Fields, appErr.Message)
			if !appErr.GetMergeStatus() {
				return acc, de, true
			}
			acc, _, _ := generateClientFields(appErr.Unwrap(), acc)
			return acc, de, true
		}
	}

	switch t := err.(type) {
	case interface{ Unwrap() error }:

		return generateClientFields(t.Unwrap(), acc)

	case interface{ Unwrap() []error }:
		var firstErr3 detailedError
		foundFirstErr3 := false
		for _, e := range t.Unwrap() {
			errMessage3, detailedErr3, found3 := generateClientFields(e, errorMessage{})
			acc = acc.accumulate(errMessage3.Fields, errMessage3.Message)
			if found3 && !foundFirstErr3 {
				firstErr3 = detailedErr3
				foundFirstErr3 = true
			}
		}
		return acc, firstErr3, foundFirstErr3
	}

	return acc, nil, false
}

func CreateClientErr(err error) (AppError, bool) {
	errMsg, detailedErr, ok := generateClientFields(err, errorMessage{})
	if !ok {
		return AppError{}, ok
	}
	mergeAppErr := detailedErr.GiveErr()
	mergeAppErr.Fields = errMsg.Fields
	mergeAppErr.Message = errMsg.Message
	return mergeAppErr, true
}
