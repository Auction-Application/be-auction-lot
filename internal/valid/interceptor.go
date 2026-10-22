package valid

import (
	"context"
	"errors"

	"buf.build/go/protovalidate"
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
				return nil, status.Error(codes.Internal, "invalid request")
			}

			return nil, st.Err()

		}

		return handler(ctx, req)
	}
}
