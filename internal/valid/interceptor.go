package valid

import (
	"context"
	"errors"
	"fmt"

	"buf.build/go/protovalidate"
	"github.com/Auction-Application/be-auction-item/internal/apperror"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func Validate(v protovalidate.Validator) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		msg, ok := req.(proto.Message)
		if !ok {
			return nil, status.Error(codes.Internal, "unexpected request type")
		}
		if err := v.Validate(msg); err != nil {
			var valErr *protovalidate.ValidationError
			if !errors.As(err, &valErr) {
				return nil, status.Error(codes.Internal, "validation failed")
			}

			br := &errdetails.BadRequest{}
			for _, viol := range valErr.Violations {
				br.FieldViolations = append(br.FieldViolations, &errdetails.BadRequest_FieldViolation{
					Field:       protovalidate.FieldPathString(viol.Proto.GetField()),
					Description: viol.Proto.GetMessage(),
				})
			}

			st, detailsErr := status.New(codes.InvalidArgument, "invalid request").WithDetails(br)
			if detailsErr != nil {
				return nil, status.Error(codes.Internal, "invalid request and internal error")
			}

			return nil, st.Err()

		}

		return handler(ctx, req)
	}
}

func clientCodeInvalidArgumentTranslation(err apperror.AppError) error {
	br := &errdetails.BadRequest{}
	for _, viol := range err.Fields {
		br.FieldViolations = append(br.FieldViolations, &errdetails.BadRequest_FieldViolation{
			Field:       viol.Field,
			Description: viol.Description,
		})
	}

	st, detailsErr := status.New(codes.InvalidArgument, err.Message).WithDetails(br)
	if detailsErr != nil {
		return status.Error(codes.Internal, "invalid request and internal error")
	}

	return st.Err()
}

func clientCodeStorageConflictTranslation(err apperror.AppError) error {
	preCond := &errdetails.PreconditionFailure{}
	for _, viol := range err.Fields {
		preCond.Violations = append(preCond.Violations, &errdetails.PreconditionFailure_Violation{
			Subject:     viol.Field,
			Description: viol.Description,
		})
	}

	// todo make message dynamic
	st, detailsErr := status.New(codes.FailedPrecondition, err.Message).WithDetails(preCond)
	if detailsErr != nil {
		return status.Error(codes.Internal, "precondition failed and internal error")
	}

	return st.Err()
}

func codeInternalServerErrorTranslation(err apperror.AppError) error {
	return status.Error(codes.Internal, "internal server error")
}

func codeUnknownErrTranslation(err apperror.AppError) error {
	return status.Error(codes.Unknown, "unknown error")
}

var appErrToGRPCTranslation = map[apperror.ErrCode]func(err apperror.AppError) error{
	apperror.ClientCodeInvalidArgument: clientCodeInvalidArgumentTranslation,
	apperror.ClientCodeStorageConflict: clientCodeStorageConflictTranslation,
	apperror.CodeInternalServerError:   codeInternalServerErrorTranslation,
	apperror.CodeUnknownError:          codeUnknownErrTranslation,
}

func convertToGRPCErr(err apperror.AppError) error {
	const op apperror.Operation = "valid.convertToGRPCErr"
	translationFn, ok := appErrToGRPCTranslation[err.Code]

	if !ok {
		return codeInternalServerErrorTranslation(apperror.InternalServerError(op, err, "grpcTranslation code failed"))
	}
	return translationFn(err)
}

// priority is from left to right, left most has higher priority than the right and so on.
// var priorityClientDisplayErrCodes []apperror.ErrCode = []apperror.ErrCode{apperror.ClientCodeInvalidArgument, apperror.ClientCodeStorageConflict}

type errorMessage struct {
	Message string
	Fields  []apperror.FieldError
}

func searchCode(err error, errCollection errorMessage) (errorMessage, apperror.DetailedError, bool) {
	var errCollection1 errorMessage

	// }
	// if len(errCollection) > 0 {
	// 	mergeAppErr := apperror.AppError{Code: errCollection[0].Code, Err: err, Message: errCollection[0].Message}
	// 	for i, e := range errCollection {
	// 		mergeAppErr.Fields = append(mergeAppErr.Fields, e.Fields...)
	// 		if i > 0 {
	// 			mergeAppErr.Message = mergeAppErr.Message + ";" + e.Message
	// 		}
	// 	}
	// 	return mergeAppErr
	// }

	///////////////////////////////

	detailedErr, ok := err.(apperror.DetailedError)
	if ok {
		if detailedErr.GiveErr().IsClientErr() {
			if detailedErr.GiveErr().GetMergeStatus() {
				errCollection1.Fields = append(errCollection.Fields, detailedErr.GiveErr().Fields...)
				errCollection1.Message = errCollection.Message + detailedErr.GiveErr().Message

				return searchCode(err, errCollection1)
			} else {
				return errorMessage{}, detailedErr.GiveErr(), ok
			}
		}
	}

	switch t := err.(type) {
	case interface{ Unwrap() error }:
		return searchCode(t.Unwrap(), errCollection)

	case interface{ Unwrap() []error }:
		for _, e := range t.Unwrap() {
			errMessageUnwrap, detailedErrUnwrap, isOk := searchCode(e, errCollection)
			if !isOk {
				continue
			}
			errCollection1.Fields = append(errMessageUnwrap.Fields, detailedErrUnwrap.GiveErr().Fields...)
			errCollection1.Message = errMessageUnwrap.Message + detailedErrUnwrap.GiveErr().Message
			// errCollection1 = append(errCollection1, searchCode(e, errCollection)...)
		}

	}

	return errCollection1, detailedErr, ok
}

func prioritySearchErrorCode(err error) apperror.AppError {
	return apperror.AppError{}
	// var errCollection []apperror.AppError
	// for _, code := range priorityClientDisplayErrCodes {
	// appErr, mergeStatus, ok := searchCode(err)
	// if ok {
	// 	if !mergeStatus && len(errCollection) < 1 {
	// 		return appErr
	// 	}
	// 	errCollection = append(errCollection, appErr)
	// }

	// // }
	// if len(errCollection) > 0 {
	// 	mergeAppErr := apperror.AppError{Code: errCollection[0].Code, Err: err, Message: errCollection[0].Message}
	// 	for i, e := range errCollection {
	// 		mergeAppErr.Fields = append(mergeAppErr.Fields, e.Fields...)
	// 		if i > 0 {
	// 			mergeAppErr.Message = mergeAppErr.Message + ";" + e.Message
	// 		}
	// 	}
	// 	return mergeAppErr
	// }
	// const operation apperror.Operation = "valid.prioritySearchErrorCode"
	// return apperror.CatchAllUnknownServerError(operation, err, "unknown error")
}

func toGRPCError(err error) error {
	if err == nil {
		return nil
	}

	if _, ok := status.FromError(err); ok {
		return err
	}

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "deadline exceeded")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request cancelled")
	}

	appError := prioritySearchErrorCode(err)
	// todo add logging
	fmt.Println(appError)
	return convertToGRPCErr(appError)
}

// todo add loggger in errorInterceptor
func ErrorResponseInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		response, err := handler(ctx, req)
		if err == nil {
			return response, nil
		}

		return nil, toGRPCError(err)
	}
}
