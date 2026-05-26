package server

import (
	"fmt"
	"net"

	"github.com/Auction-Application/be-auction-item/internal/database"
	lotPb "github.com/Auction-Application/be-auction-item/rpc/gen/lot/v1"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
)

type LotServer struct {
	lotPb.UnimplementedLotServiceServer
	database *pgx.Conn
}

func NewLotServer() *LotServer {
	return &LotServer{
		database: database.ConnectToDB(),
	}
}

func Run(port int) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))

	if err != nil {
		fmt.Println("failed to start the server")
		return err
	}

	defer listener.Close()

	lotServer := NewLotServer()

	grpcServer := grpc.NewServer()
	lotPb.RegisterLotServiceServer(grpcServer, lotServer)

	if err := grpcServer.Serve(listener); err != nil {
		fmt.Println("error in listening the server")
		return err
	}

	return nil

}
