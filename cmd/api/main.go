package main

import (
	"fmt"
	"net"

	"github.com/Auction-Application/be-auction-item/internal/database"
	"github.com/Auction-Application/be-auction-item/internal/imagestore"
	"github.com/Auction-Application/be-auction-item/internal/server"
	lotPb "github.com/Auction-Application/be-auction-item/rpc/gen/lot/v1"
	lotimage "github.com/Auction-Application/be-auction-item/rpc/gen/lot_image/v1"
	"github.com/joho/godotenv"
	"google.golang.org/grpc"
)

func main() {
	if err := godotenv.Load(); err != nil {
		fmt.Println("No .env found, when using environment variables")
		fmt.Println(err)
		return
	}

	fmt.Println("grpc server about to be run")
	err := Run(9002, createRpc())
	if err != nil {
		fmt.Println("Cannot run the grpc server")
		fmt.Println(err)
		return
	}
}

type Rpc struct {
	lotServer  *server.LotServer
	imageStore *imagestore.ImageStore
}

func createRpc() Rpc {
	databaseConnection := database.ConnectToDB()
	rpc := Rpc{
		lotServer:  server.NewLotServer(databaseConnection),
		imageStore: imagestore.NewImageStore(databaseConnection),
	}
	return rpc
}

func Run(port int, rpc Rpc) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		fmt.Println("failed to start the server")
		return err
	}

	defer listener.Close()

	grpcServer := grpc.NewServer()
	lotPb.RegisterLotServiceServer(grpcServer, rpc.lotServer)
	lotimage.RegisterLotImageServiceServer(grpcServer, rpc.imageStore)

	fmt.Println("Listening...")

	if err := grpcServer.Serve(listener); err != nil {
		fmt.Println("error in listening the server")
		return err
	}

	return nil
}
