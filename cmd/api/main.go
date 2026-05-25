package main

import (
	"fmt"

	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil {
		fmt.Println("No .env found, when using environment variables")
		fmt.Println(err)
		return
	}

	fmt.Println("gRPC server up and ready...")
	// err := Run(9001, server.NewUserServer())
	// if err != nil {
	// 	fmt.Println("Cannot run the server")
	// 	return
	// }
}
