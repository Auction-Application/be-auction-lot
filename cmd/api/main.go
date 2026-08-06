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

// var files = []server.UploadFile{
// 	{
// 		Sha256:       "abc123",
// 		FileName:     "photo1.jpg",
// 		ClientFileId: "client-id-1",
// 		FileSize:     204800,
// 	},
// 	{
// 		Sha256:       "def456",
// 		FileName:     "photo2.jpg",
// 		ClientFileId: "client-id-2",
// 		FileSize:     512000,
// 	},
// 	{
// 		Sha256:       "abc123",
// 		FileName:     "photo11.jpg",
// 		ClientFileId: "client-id-11",
// 		FileSize:     2048001,
// 	},
// 	{
// 		Sha256:       "def4567",
// 		FileName:     "photo2.jpg",
// 		ClientFileId: "client-id-2",
// 		FileSize:     512000,
// 	},
// 	{
// 		Sha256:       "def456",
// 		FileName:     "photo26.jpg",
// 		ClientFileId: "client-id-26",
// 		FileSize:     5120006,
// 	},
// }

func main() {
	if err := godotenv.Load(); err != nil {
		fmt.Println("No .env found, when using environment variables")
		fmt.Println(err)
		return
	}
	// server.IntentBatchUpload(files)
	// s3Storage, err := server.NewS3Storage()
	// if err != nil {
	// 	fmt.Println("Error in creating s3 client")
	// }
	// err = s3Storage.UploadFile(context.TODO(), "arn:aws:s3:ap-south-1:433154991296:accesspoint/auction-lot-service-access-point", "test1", "files/aws_logo.png")
	// if err != nil {
	// 	fmt.Println("Error in uploading file")
	// }

	// presignedHttpRequest, err := s3Storage.CreatePresignedPutObjectUrl(context.Background(), "arn:aws:s3:ap-south-1:433154991296:accesspoint/auction-lot-service-access-point", "test2", 3600)

	// if err != nil {
	// 	fmt.Println("Error in creating preSigned put s3 url")
	// }

	// fmt.Printf("%#v", presignedHttpRequest)

	fmt.Println("grpc server about to be run")
	err := Run(9002, createRpc())
	if err != nil {
		fmt.Println("Cannot run the grpc server")
		fmt.Println(err)
		return
	}

}

type Rpc struct {
	// conn       *pgx.Conn
	lotServer  *server.LotServer
	imageStore *imagestore.ImageStore
}

func createRpc() Rpc {
	databaseConnection := database.ConnectToDB()
	rpc := Rpc{
		// conn:       databaseConnection,
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
