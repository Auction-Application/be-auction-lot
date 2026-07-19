package server

import (
	"context"
	"fmt"

	"buf.build/go/protovalidate"
	"github.com/Auction-Application/be-auction-item/internal/database/auctionLotTableQuery"
	lotPb "github.com/Auction-Application/be-auction-item/rpc/gen/lot/v1"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type LotServer struct {
	lotPb.UnimplementedLotServiceServer
	dbStorageQuery *auctionLotTableQuery.Queries
}

func (s *LotServer) CreateLot(ctx context.Context, req *lotPb.CreateLotRequest) (*lotPb.CreateLotResponse, error) {
	if err := protovalidate.Validate(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid lot payload")
	}

	id, err := s.dbStorageQuery.CreateLot(ctx, auctionLotTableQuery.CreateLotParams{Title: *req.Lot.LotTitle, Description: *req.Lot.Description, Category: req.Lot.Category, BidOpeningPrice: req.Lot.BidOpeningPrice})
	if err != nil {
		fmt.Println(err)
		return nil, status.Error(codes.Internal, "error creating lot")
	}

	return &lotPb.CreateLotResponse{Success: new(true), Message: new("Lot Created"), LotId: new(id.String())}, nil

}

func NewLotServer(databaseConnection *pgx.Conn) *LotServer {
	return &LotServer{
		dbStorageQuery: auctionLotTableQuery.New(databaseConnection),
	}
}
